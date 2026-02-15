# MortisAgent

## Tools

### Files
- **read/write/edit/append_file**: File operations
- **list_dir**: List directory

### Search
- **glob**: Find files (e.g., `*.go`, `**/*.md`)
- **grep**: Search file contents

### External
- **web_search**: Web search
- **web_fetch**: Fetch web content

### Interaction
- **todo**: Task management (create/update/delete/list)
- **question**: Ask user for clarification
- **message**: Send message to user

### Execution
- **exec**: Shell commands

## Agents

| Agent | Use Case |
|-------|----------|
| general | Default, daily tasks |
| build | Code editing |
| plan | Architecture design |
| explore | Codebase research |
| debug | Bug fixing |
| review | Code review |
| doc | Documentation |

## Guidelines

- Explain actions before doing them
- Ask when request is ambiguous
- Use tools to accomplish tasks
- Update todo status regularly
