package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	rfeed "github.com/gorilla/feeds"
	"github.com/mmcdole/gofeed"
)

// --- Data Model ---

type Feed struct {
	Name       string        `json:"name"`
	URL        string        `json:"url"`
	GroupLogic string        `json:"group_logic"`
	Groups     []FilterGroup `json:"groups"`
}

type FilterGroup struct {
	Logic string `json:"logic"`
	Rules []Rule `json:"rules"`
}

type Rule struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// --- Persistence ---

var dataFile = "/data/feeds.json"

func loadFeeds() ([]Feed, error) {
	data, err := os.ReadFile(dataFile)
	if os.IsNotExist(err) {
		return []Feed{}, nil
	}
	if err != nil {
		return nil, err
	}
	var feeds []Feed
	return feeds, json.Unmarshal(data, &feeds)
}

func saveFeeds(feeds []Feed) error {
	data, err := json.MarshalIndent(feeds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dataFile, data, 0644)
}

// --- Filter Engine ---

func joinByLogic(parts []string, logic string) string {
	switch logic {
	case "any":
		return strings.Join(parts, " || ")
	case "none":
		return "!(" + strings.Join(parts, " || ") + ")"
	default:
		if logic != "all" && logic != "" {
			log.Printf("buildExpr: unknown logic %q, defaulting to all", logic)
		}
		return strings.Join(parts, " && ")
	}
}

func buildExpr(feed Feed) string {
	var groupExprs []string

	for _, group := range feed.Groups {
		if len(group.Rules) == 0 {
			continue
		}

		var ruleExprs []string
		for _, rule := range group.Rules {
			val := strings.ToLower(rule.Value)
			field := rule.Field
			var e string
			switch rule.Operator {
			case "contains":
				e = fmt.Sprintf(`lower(%s) contains "%s"`, field, val)
			case "not_contains":
				e = fmt.Sprintf(`!(lower(%s) contains "%s")`, field, val)
			case "equals":
				e = fmt.Sprintf(`lower(%s) == "%s"`, field, val)
			case "not_equals":
				e = fmt.Sprintf(`lower(%s) != "%s"`, field, val)
			default:
				log.Printf("buildExpr: unknown operator %q, defaulting to contains", rule.Operator)
				e = fmt.Sprintf(`lower(%s) contains "%s"`, field, val)
			}
			ruleExprs = append(ruleExprs, e)
		}

		groupExprs = append(groupExprs, joinByLogic(ruleExprs, group.Logic))
	}

	if len(groupExprs) == 0 {
		return "true"
	}

	if len(groupExprs) == 1 {
		return groupExprs[0]
	}

	wrapped := make([]string, len(groupExprs))
	for i, g := range groupExprs {
		wrapped[i] = "(" + g + ")"
	}

	return joinByLogic(wrapped, feed.GroupLogic)
}

// ruleFields extracts unique field names referenced across all rules in a feed.
func ruleFields(feed Feed) []string {
	seen := make(map[string]bool)
	var fields []string
	for _, group := range feed.Groups {
		for _, rule := range group.Rules {
			if !seen[rule.Field] {
				seen[rule.Field] = true
				fields = append(fields, rule.Field)
			}
		}
	}
	return fields
}

// itemToEnv builds a flat map[string]string from a gofeed.Item for expr evaluation.
// fields lists the keys that must be present (defaulting to "").
func itemToEnv(item *gofeed.Item, fields []string) map[string]string {
	env := make(map[string]string, len(fields))
	for _, f := range fields {
		env[f] = ""
	}

	// Standard fields
	if _, ok := env["title"]; ok {
		env["title"] = item.Title
	}
	if _, ok := env["link"]; ok {
		env["link"] = item.Link
	}
	if _, ok := env["description"]; ok {
		env["description"] = item.Description
	}
	if _, ok := env["content"]; ok {
		env["content"] = item.Content
	}
	if _, ok := env["author"]; ok && item.Author != nil {
		env["author"] = item.Author.Name
	}
	needCats := false
	if _, ok := env["categories"]; ok {
		needCats = true
	}
	if _, ok := env["category"]; ok {
		needCats = true
	}
	if needCats {
		cats := strings.Join(item.Categories, ",")
		env["categories"] = cats
		env["category"] = cats
	}

	// Custom (non-namespaced) XML tags
	for k, v := range item.Custom {
		if _, ok := env[k]; ok {
			env[k] = v
		}
	}

	return env
}

// filterItems applies the feed's filter rules to items and returns those that match.
// On compile error it fails-open and returns all items.
func filterItems(items []*gofeed.Item, feed Feed) []*gofeed.Item {
	exprStr := buildExpr(feed)

	if exprStr == "true" {
		return items
	}

	fields := ruleFields(feed)

	// Build prototype env (all fields → empty string) for type-checking compilation.
	proto := make(map[string]string, len(fields))
	for _, f := range fields {
		proto[f] = ""
	}

	program, err := expr.Compile(exprStr, expr.Env(proto), expr.AsBool())
	if err != nil {
		log.Printf("filterItems: compile error for feed %q: %v", feed.Name, err)
		return items
	}

	var out []*gofeed.Item
	for _, item := range items {
		env := itemToEnv(item, fields)
		result, err := expr.Run(program, env)
		if err != nil {
			log.Printf("filterItems: eval error: %v", err)
			continue
		}
		if result.(bool) {
			out = append(out, item)
		}
	}
	return out
}

// --- Templates ---

const rulePartial = `<fieldset class="rule">
<legend>Rule</legend>
<div class="form-group">
<label>Field</label>
<input type="text" name="field" placeholder="e.g. title, location">
</div>
<div class="form-group">
<select name="operator">
<option value="contains">contains</option>
<option value="not_contains">not contains</option>
<option value="equals">equals</option>
<option value="not_equals">not equals</option>
</select>
</div>
<div class="form-group">
<label>Value</label>
<input type="text" name="value" placeholder="e.g. Remote, Designer">
</div>
<div class="remove-rule" style="text-align:right"><a href="#" onclick="this.closest('.rule').remove();return false">remove rule</a></div>
</fieldset>`

var groupPartial = `<fieldset class="group">
<legend>Group</legend>
<div class="form-group group-logic" style="display:none">
<label>Rules must match</label>
<select name="group_logic">
<option value="all">all (AND)</option>
<option value="any" selected>any (OR)</option>
<option value="none">none (NOR)</option>
</select>
</div>
<div class="rules">` + rulePartial + `</div>
<a href="#" hx-get="/partials/rule" hx-target="previous .rules" hx-swap="beforeend">+ rule</a><span class="remove-group" style="float:right"><a href="#" onclick="this.closest('.group').remove();return false">remove group</a></span>
</fieldset>`

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"ruleCount": func(groups []FilterGroup) int {
		n := 0
		for _, g := range groups {
			n += len(g.Rules)
		}
		return n
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RSS Filter</title>
<link rel="stylesheet" href="https://unpkg.com/terminal.css@0.7.5/dist/terminal.min.css">
<script src="https://unpkg.com/htmx.org@2.0.4"></script>
</head>
<body class="terminal">
<main>
<h1>RSS Filter</h1>

<section id="feed-form">
{{template "form" .}}
</section>

<section style="margin-top:calc(var(--global-space) * 4)">
<h2>Feeds</h2>
{{if .Feeds}}
<table>
<tbody>
{{range .Feeds}}
<tr>
<td>{{.Name}}</td>
<td style="text-align:right"><a href="{{$.Host}}/api/feed?name={{urlquery .Name}}">url</a> · <a href="#" hx-get="/api/edit?name={{urlquery .Name}}" hx-target="#feed-form" hx-swap="innerHTML">edit ({{ruleCount .Groups}})</a> · <a href="#" hx-post="/api/delete?name={{urlquery .Name}}" hx-target="closest tr" hx-swap="delete" hx-confirm="Delete {{.Name}}?">delete</a></td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<p>No feeds yet.</p>
{{end}}
</section>
</main>

<script>
document.addEventListener("submit", function(e) {
  if (e.target.id !== "form") return;
  e.preventDefault();
  var form = e.target;
  var data = {
    name: form.querySelector('[name="name"]').value,
    url: form.querySelector('[name="url"]').value,
    group_logic: form.querySelector('[name="group_logic"]').value,
    groups: []
  };
  form.querySelectorAll('.group').forEach(function(g) {
    var group = {
      logic: g.querySelector('[name="group_logic"]').value,
      rules: []
    };
    g.querySelectorAll('.rule').forEach(function(r) {
      group.rules.push({
        field: r.querySelector('[name="field"]').value,
        operator: r.querySelector('[name="operator"]').value,
        value: r.querySelector('[name="value"]').value
      });
    });
    data.groups.push(group);
  });
  fetch(form.action, {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(data)
  }).then(function(resp) {
    if (resp.ok) location.reload();
    else resp.text().then(function(t) { alert("Error: " + t); });
  });
});
function updateVisibility() {
  var groups = document.querySelectorAll('#groups .group');
  var wrap = document.getElementById('group-logic-wrap');
  if (wrap) wrap.style.display = groups.length >= 2 ? '' : 'none';
  groups.forEach(function(g) {
    var gl = g.querySelector('.group-logic');
    var rules = g.querySelectorAll('.rule');
    if (gl) gl.style.display = rules.length >= 2 ? '' : 'none';
    g.querySelectorAll('.remove-rule').forEach(function(r) {
      r.style.display = rules.length >= 2 ? '' : 'none';
    });
    var rg = g.querySelector('.remove-group');
    if (rg) rg.style.display = groups.length >= 2 ? '' : 'none';
  });
}
new MutationObserver(updateVisibility).observe(
  document.getElementById('groups') || document.body,
  {childList: true, subtree: true}
);
updateVisibility();
</script>
</body>
</html>

{{define "form"}}
<form id="form" action="{{if .Edit}}/api/save?name={{urlquery .Edit.Name}}{{else}}/feeds{{end}}" method="post">
<fieldset>
<legend>{{if .Edit}}Edit Feed{{else}}New Feed{{end}}</legend>
<div class="form-group">
<label for="name">Name</label>
<input id="name" type="text" name="name" {{if .Edit}}value="{{.Edit.Name}}" readonly{{else}}required placeholder="my-feed"{{end}}>
</div>
<div class="form-group">
<label for="url">URL</label>
<input id="url" type="text" name="url" {{if .Edit}}value="{{.Edit.URL}}"{{else}}placeholder="https://example.com/feed.xml"{{end}} required>
</div>
<div class="form-group" id="group-logic-wrap"{{if .Edit}}{{if lt (len .Edit.Groups) 2}} style="display:none"{{end}}{{else}} style="display:none"{{end}}>
<label for="group_logic">Groups must match</label>
<select id="group_logic" name="group_logic">
{{if .Edit}}<option value="all"{{if eq .Edit.GroupLogic "all"}} selected{{end}}>all</option>
<option value="any"{{if eq .Edit.GroupLogic "any"}} selected{{end}}>any</option>
<option value="none"{{if eq .Edit.GroupLogic "none"}} selected{{end}}>none</option>
{{else}}<option value="all">all</option>
<option value="any" selected>any</option>
<option value="none">none</option>
{{end}}</select>
</div>
<div id="groups">
{{if .Edit}}{{range $gi, $g := .Edit.Groups}}<fieldset class="group">
<legend>Group</legend>
<div class="form-group group-logic"{{if lt (len $g.Rules) 2}} style="display:none"{{end}}>
<label>Rules must match</label>
<select name="group_logic">
<option value="all"{{if eq $g.Logic "all"}} selected{{end}}>all (AND)</option>
<option value="any"{{if eq $g.Logic "any"}} selected{{end}}>any (OR)</option>
<option value="none"{{if eq $g.Logic "none"}} selected{{end}}>none (NOR)</option>
</select>
</div>
<div class="rules">
{{range $g.Rules}}<fieldset class="rule">
<legend>Rule</legend>
<div class="form-group">
<label>Field</label>
<input type="text" name="field" value="{{.Field}}" placeholder="e.g. title, location">
</div>
<div class="form-group">
<select name="operator">
<option value="contains"{{if eq .Operator "contains"}} selected{{end}}>contains</option>
<option value="not_contains"{{if eq .Operator "not_contains"}} selected{{end}}>not contains</option>
<option value="equals"{{if eq .Operator "equals"}} selected{{end}}>equals</option>
<option value="not_equals"{{if eq .Operator "not_equals"}} selected{{end}}>not equals</option>
</select>
</div>
<div class="form-group">
<label>Value</label>
<input type="text" name="value" value="{{.Value}}" placeholder="e.g. Remote, Designer">
</div>
<div class="remove-rule" style="text-align:right"><a href="#" onclick="this.closest('.rule').remove();return false">remove rule</a></div>
</fieldset>{{end}}
</div>
<a href="#" hx-get="/partials/rule" hx-target="previous .rules" hx-swap="beforeend">+ rule</a><span class="remove-group" style="float:right"><a href="#" onclick="this.closest('.group').remove();return false">remove group</a></span>
</fieldset>{{end}}{{else}}` + groupPartial + `{{end}}
</div>
<div class="form-group">
<a href="#" hx-get="/partials/group" hx-target="#groups" hx-swap="beforeend">+ group</a>
</div>
{{if .Edit}}<hr>{{end}}
<div class="form-group">
<button type="submit" class="btn btn-default{{if not .Edit}} btn-block{{end}}">{{if .Edit}}Save{{else}}Create Feed{{end}}</button>
</div>
{{if .Edit}}<a href="#" onclick="location.reload();return false">cancel</a>{{end}}
</fieldset>
</form>
{{end}}`))

// --- HTTP Handlers ---

// findFeed loads feeds and returns the index of the named feed (-1 if not found).
func findFeed(name string) ([]Feed, int, error) {
	feeds, err := loadFeeds()
	if err != nil {
		return nil, -1, err
	}
	for i, f := range feeds {
		if f.Name == name {
			return feeds, i, nil
		}
	}
	return feeds, -1, nil
}

// feedName extracts the feed name from query param (?name=) or path param ({name}).
func feedName(r *http.Request) string {
	if n := r.URL.Query().Get("name"); n != "" {
		return n
	}
	return r.PathValue("name")
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	feeds, err := loadFeeds()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	host := "http://" + r.Host
	if err := indexTmpl.Execute(w, map[string]any{"Feeds": feeds, "Host": host, "Edit": nil}); err != nil {
		log.Printf("handleIndex: template error: %v", err)
	}
}

func handleCreate(w http.ResponseWriter, r *http.Request) {
	var feed Feed
	if err := json.NewDecoder(r.Body).Decode(&feed); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	feeds, idx, err := findFeed(feed.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if idx >= 0 {
		http.Error(w, "feed already exists", http.StatusConflict)
		return
	}
	feeds = append(feeds, feed)
	if err := saveFeeds(feeds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := feedName(r)
	var feed Feed
	if err := json.NewDecoder(r.Body).Decode(&feed); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	feeds, idx, err := findFeed(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if idx < 0 {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	feed.Name = name
	feeds[idx] = feed
	if err := saveFeeds(feeds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	feeds, idx, err := findFeed(feedName(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if idx < 0 {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	feeds = append(feeds[:idx], feeds[idx+1:]...)
	if err := saveFeeds(feeds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func handleEdit(w http.ResponseWriter, r *http.Request) {
	feeds, idx, err := findFeed(feedName(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if idx < 0 {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err := indexTmpl.ExecuteTemplate(w, "form", map[string]any{"Edit": &feeds[idx]}); err != nil {
		log.Printf("handleEdit: template error: %v", err)
	}
}

var feedParser = func() *gofeed.Parser {
	p := gofeed.NewParser()
	p.Client = &http.Client{Timeout: 15 * time.Second}
	return p
}()

func handleFeedXML(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(feedName(r), ".xml")
	feeds, idx, err := findFeed(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if idx < 0 {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	parsed, err := feedParser.ParseURL(feeds[idx].URL)
	if err != nil {
		http.Error(w, "failed to fetch upstream feed: "+err.Error(), http.StatusBadGateway)
		return
	}
	filtered := filterItems(parsed.Items, feeds[idx])

	out := &rfeed.Feed{
		Title:       parsed.Title,
		Link:        &rfeed.Link{Href: parsed.Link},
		Description: parsed.Description,
	}
	for _, item := range filtered {
		fi := &rfeed.Item{
			Title:       item.Title,
			Link:        &rfeed.Link{Href: item.Link},
			Description: item.Description,
			Id:          item.GUID,
		}
		if item.PublishedParsed != nil {
			fi.Created = *item.PublishedParsed
		}
		out.Items = append(out.Items, fi)
	}

	rss, err := out.ToRss()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(rss))
}

func handlePartialGroup(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(groupPartial))
}

func handlePartialRule(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(rulePartial))
}

// --- Main ---

func main() {
	if v := os.Getenv("DATA_FILE"); v != "" {
		dataFile = v
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("POST /feeds", handleCreate)
	mux.HandleFunc("POST /api/save", handleUpdate)
	mux.HandleFunc("POST /api/delete", handleDelete)
	mux.HandleFunc("GET /api/edit", handleEdit)
	mux.HandleFunc("GET /api/feed", handleFeedXML)
	mux.HandleFunc("GET /feeds/{name}", handleFeedXML)
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /partials/group", handlePartialGroup)
	mux.HandleFunc("GET /partials/rule", handlePartialRule)
	log.Println("rss-filter listening on :4080")
	log.Fatal(http.ListenAndServe(":4080", mux))
}
