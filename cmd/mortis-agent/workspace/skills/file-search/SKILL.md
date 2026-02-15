# Search Tools

## Glob - Find Files

```json
{"pattern": "*.go"}           // Current dir
{"pattern": "**/*.go"}        // Recursive
{"pattern": "src/**/*.js"}    // Subdir
```

## Grep - Search Content

```json
{"pattern": "func main", "include": "*.go"}
{"pattern": "TODO", "exclude": "*_test.go"}
```

## Patterns

| Pattern | Meaning |
|---------|---------|
| `*` | Any chars |
| `**` | Recursive |
| `?` | Single char |
| `{a,b}` | a or b |
