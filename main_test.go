package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
)

func TestLoadSaveFeeds(t *testing.T) {
	dataFile = filepath.Join(t.TempDir(), "feeds.json")

	// Load from non-existent file returns empty slice
	feeds, err := loadFeeds()
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 0 {
		t.Fatalf("expected 0 feeds, got %d", len(feeds))
	}

	// Save and reload
	feeds = []Feed{{
		Name: "test", URL: "https://example.com/rss.xml", GroupLogic: "all",
		Groups: []FilterGroup{{
			Logic: "all",
			Rules: []Rule{
				{Field: "title", Operator: "contains", Value: "Go"},
			},
		}},
	}}
	if err := saveFeeds(feeds); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadFeeds()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(loaded))
	}
	if loaded[0].Name != "test" {
		t.Fatalf("expected name 'test', got %q", loaded[0].Name)
	}
	if len(loaded[0].Groups) != 1 || len(loaded[0].Groups[0].Rules) != 1 {
		t.Fatal("group/rule structure not preserved")
	}
	if loaded[0].Groups[0].Rules[0].Field != "title" {
		t.Fatalf("expected field 'title', got %q", loaded[0].Groups[0].Rules[0].Field)
	}
}

func TestBuildExpr(t *testing.T) {
	cases := []struct {
		name string
		feed Feed
		want string
	}{
		{
			name: "no groups returns true",
			feed: Feed{GroupLogic: "all"},
			want: "true",
		},
		{
			name: "single rule equals",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "all",
					Rules: []Rule{{Field: "title", Operator: "equals", Value: "Go"}},
				}},
			},
			want: `lower(title) == "go"`,
		},
		{
			name: "AND group with two rules",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "all",
					Rules: []Rule{
						{Field: "title", Operator: "contains", Value: "Remote"},
						{Field: "categories", Operator: "contains", Value: "design"},
					},
				}},
			},
			want: `lower(title) contains "remote" && lower(categories) contains "design"`,
		},
		{
			name: "OR group",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "any",
					Rules: []Rule{
						{Field: "location", Operator: "contains", Value: "Europe"},
						{Field: "location", Operator: "contains", Value: "Germany"},
					},
				}},
			},
			want: `lower(location) contains "europe" || lower(location) contains "germany"`,
		},
		{
			name: "NONE group",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "none",
					Rules: []Rule{
						{Field: "categories", Operator: "contains", Value: "Engineering"},
					},
				}},
			},
			want: `!(lower(categories) contains "engineering")`,
		},
		{
			name: "not_contains operator",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "all",
					Rules: []Rule{{Field: "title", Operator: "not_contains", Value: "Sponsored"}},
				}},
			},
			want: `!(lower(title) contains "sponsored")`,
		},
		{
			name: "not_equals operator",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{{
					Logic: "all",
					Rules: []Rule{{Field: "workmode", Operator: "not_equals", Value: "onsite"}},
				}},
			},
			want: `lower(workmode) != "onsite"`,
		},
		{
			name: "two groups with ALL between",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Remote"}},
					},
					{
						Logic: "any",
						Rules: []Rule{
							{Field: "location", Operator: "contains", Value: "Europe"},
							{Field: "location", Operator: "contains", Value: "Germany"},
						},
					},
				},
			},
			want: `(lower(title) contains "remote") && (lower(location) contains "europe" || lower(location) contains "germany")`,
		},
		{
			name: "two groups with ANY between",
			feed: Feed{
				GroupLogic: "any",
				Groups: []FilterGroup{
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Remote"}},
					},
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Hybrid"}},
					},
				},
			},
			want: `(lower(title) contains "remote") || (lower(title) contains "hybrid")`,
		},
		{
			name: "two groups with NONE between",
			feed: Feed{
				GroupLogic: "none",
				Groups: []FilterGroup{
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Remote"}},
					},
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Hybrid"}},
					},
				},
			},
			want: `!((lower(title) contains "remote") || (lower(title) contains "hybrid"))`,
		},
		{
			name: "empty group is skipped",
			feed: Feed{
				GroupLogic: "all",
				Groups: []FilterGroup{
					{Logic: "all", Rules: []Rule{}},
					{
						Logic: "all",
						Rules: []Rule{{Field: "title", Operator: "contains", Value: "Go"}},
					},
				},
			},
			want: `lower(title) contains "go"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildExpr(tc.feed)
			if got != tc.want {
				t.Errorf("buildExpr() = %q\n\t want %q", got, tc.want)
			}
		})
	}
}

func TestFilterItems(t *testing.T) {
	makeItem := func(title, link, description string, categories []string, custom map[string]string) *gofeed.Item {
		return &gofeed.Item{
			Title:       title,
			Link:        link,
			Description: description,
			Categories:  categories,
			Custom:      custom,
		}
	}

	items := []*gofeed.Item{
		makeItem("Remote Design Lead", "https://a.com", "", []string{"design"}, map[string]string{"workmode": "remote", "location": "Europe"}),
		makeItem("Onsite Engineer", "https://b.com", "", []string{"engineering"}, map[string]string{"workmode": "onsite", "location": "Germany"}),
		makeItem("Remote Engineer Berlin", "https://c.com", "", []string{"engineering"}, map[string]string{"workmode": "remote", "location": "Germany"}),
		makeItem("Remote Product Manager", "https://d.com", "", []string{"product"}, map[string]string{"workmode": "remote", "location": "USA"}),
	}

	t.Run("AND group: remote + design category", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "all",
				Rules: []Rule{
					{Field: "workmode", Operator: "equals", Value: "remote"},
					{Field: "categories", Operator: "contains", Value: "design"},
				},
			}},
		}
		got := filterItems(items, feed)
		if len(got) != 1 || got[0].Title != "Remote Design Lead" {
			t.Errorf("expected [Remote Design Lead], got %v", titlesOf(got))
		}
	})

	t.Run("OR group: Europe or Germany location", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "any",
				Rules: []Rule{
					{Field: "location", Operator: "equals", Value: "Europe"},
					{Field: "location", Operator: "equals", Value: "Germany"},
				},
			}},
		}
		got := filterItems(items, feed)
		if len(got) != 3 {
			t.Errorf("expected 3 items, got %d: %v", len(got), titlesOf(got))
		}
	})

	t.Run("two groups combined: remote AND (Europe OR Germany)", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{
				{
					Logic: "all",
					Rules: []Rule{{Field: "workmode", Operator: "equals", Value: "remote"}},
				},
				{
					Logic: "any",
					Rules: []Rule{
						{Field: "location", Operator: "equals", Value: "Europe"},
						{Field: "location", Operator: "equals", Value: "Germany"},
					},
				},
			},
		}
		got := filterItems(items, feed)
		// Remote Design Lead (Europe), Remote Engineer Berlin (Germany)
		if len(got) != 2 {
			t.Errorf("expected 2 items, got %d: %v", len(got), titlesOf(got))
		}
	})

	t.Run("NONE group: exclude Engineering", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "none",
				Rules: []Rule{{Field: "categories", Operator: "contains", Value: "engineering"}},
			}},
		}
		got := filterItems(items, feed)
		// Should keep Remote Design Lead and Remote Product Manager
		if len(got) != 2 {
			t.Errorf("expected 2 items, got %d: %v", len(got), titlesOf(got))
		}
		for _, item := range got {
			for _, cat := range item.Categories {
				if cat == "engineering" {
					t.Errorf("item %q has engineering category but should be excluded", item.Title)
				}
			}
		}
	})

	t.Run("not_contains operator", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "all",
				Rules: []Rule{{Field: "title", Operator: "not_contains", Value: "Engineer"}},
			}},
		}
		got := filterItems(items, feed)
		// Remote Design Lead, Remote Product Manager
		if len(got) != 2 {
			t.Errorf("expected 2 items, got %d: %v", len(got), titlesOf(got))
		}
	})

	t.Run("no rules passes all items", func(t *testing.T) {
		feed := Feed{Name: "test", GroupLogic: "all", Groups: []FilterGroup{}}
		got := filterItems(items, feed)
		if len(got) != len(items) {
			t.Errorf("expected %d items, got %d", len(items), len(got))
		}
	})

	t.Run("missing field defaults to empty string", func(t *testing.T) {
		itemNoCustom := makeItem("Plain Item", "https://e.com", "", nil, nil)
		feed := Feed{
			Name:       "test",
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "all",
				Rules: []Rule{{Field: "workmode", Operator: "equals", Value: "remote"}},
			}},
		}
		got := filterItems([]*gofeed.Item{itemNoCustom}, feed)
		if len(got) != 0 {
			t.Errorf("expected 0 items, got %d", len(got))
		}
	})
}

func titlesOf(items []*gofeed.Item) []string {
	titles := make([]string, len(items))
	for i, item := range items {
		titles[i] = item.Title
	}
	return titles
}

func TestHandlers(t *testing.T) {
	// Point dataFile at a temp file for test isolation
	dataFile = filepath.Join(t.TempDir(), "feeds.json")

	// Test RSS server with custom tags
	rssServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
<title>Test Jobs</title>
<link>https://example.com</link>
<description>Test</description>
<item>
<title>Designer Remote EU</title>
<category>Design</category>
<workmode>Remote</workmode>
<location>Europe</location>
</item>
<item>
<title>Engineer Onsite US</title>
<category>Engineering</category>
<workmode>On-site</workmode>
<location>USA</location>
</item>
</channel>
</rss>`))
	}))
	defer rssServer.Close()

	t.Run("health", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		handleHealth(w, req)
		if w.Body.String() != "ok" {
			t.Errorf("expected 'ok', got %q", w.Body.String())
		}
	})

	t.Run("create feed", func(t *testing.T) {
		feed := Feed{
			Name:       "test",
			URL:        rssServer.URL,
			GroupLogic: "all",
			Groups: []FilterGroup{{
				Logic: "all",
				Rules: []Rule{
					{Field: "workmode", Operator: "equals", Value: "Remote"},
					{Field: "location", Operator: "equals", Value: "Europe"},
				},
			}},
		}
		body, _ := json.Marshal(feed)
		req := httptest.NewRequest("POST", "/feeds", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleCreate(w, req)
		if w.Code != http.StatusCreated {
			t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("create duplicate returns 409", func(t *testing.T) {
		feed := Feed{Name: "test", URL: rssServer.URL, GroupLogic: "all"}
		body, _ := json.Marshal(feed)
		req := httptest.NewRequest("POST", "/feeds", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handleCreate(w, req)
		if w.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("serve filtered feed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/feeds/test.xml", nil)
		req.SetPathValue("name", "test.xml")
		w := httptest.NewRecorder()
		handleFeedXML(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "Designer Remote EU") {
			t.Error("matching item 'Designer Remote EU' should be present")
		}
		if strings.Contains(body, "Engineer Onsite US") {
			t.Error("non-matching item 'Engineer Onsite US' should be absent")
		}
		// Custom tags should not appear in output
		if strings.Contains(body, "<workmode>") {
			t.Error("custom tag <workmode> should not appear in output")
		}
		if strings.Contains(body, "<location>") {
			t.Error("custom tag <location> should not appear in output")
		}
	})

	t.Run("delete feed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/feeds/test/delete", nil)
		req.SetPathValue("name", "test")
		w := httptest.NewRecorder()
		handleDelete(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		feeds, err := loadFeeds()
		if err != nil {
			t.Fatal(err)
		}
		if len(feeds) != 0 {
			t.Errorf("expected 0 feeds after delete, got %d", len(feeds))
		}
	})

	t.Run("serve unknown feed returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/feeds/nope.xml", nil)
		req.SetPathValue("name", "nope.xml")
		w := httptest.NewRecorder()
		handleFeedXML(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
	})
}
