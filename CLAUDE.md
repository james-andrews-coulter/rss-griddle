# rss-griddle

A tiny, self-hosted RSS filter proxy.

## Development

```bash
go run main.go                    # Start on :4080
go test -v ./...                  # Run tests
go build -o rss-griddle .         # Build binary
DATA_FILE=./feeds.json go run .   # Use local data file
```

## Architecture

Single-file Go app (`main.go`, ~650 lines):

- **Data model** — `Feed`, `FilterGroup`, `Rule` structs with JSON persistence
- **Filter engine** — builds expr-lang expressions from rules, compiles and evaluates against feed items
- **HTTP handlers** — CRUD for feeds, filtered RSS XML output, HTMX partials
- **Templates** — inline Go templates with HTMX for dynamic form interactions

## Key Patterns

- `buildExpr()` converts nested rule groups into a single expr-lang expression string
- `filterItems()` compiles the expression once, evaluates per item
- All string comparisons are case-insensitive (lowercased)
- Missing fields default to empty string (fail-open on missing, fail-open on compile error)
- Single JSON file for persistence, no database
