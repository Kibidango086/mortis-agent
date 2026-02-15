# Question Tool

Ask users for clarification or choices.

## Usage

```json
{
  "question": "Which database?",
  "options": ["PostgreSQL", "MySQL", "MongoDB"],
  "timeout": 300
}
```

## When to Use

- Need more information
- Multiple valid options
- Important confirmation needed

## Example

```
Ask: {"question": "Which backend language?", "options": ["Go", "Python", "Node.js"]}
User replies: 1
Continue with Go
```
