# Todo Tool

Manage tasks with the todo tool.

## Actions

- **create**: `{"action": "create", "content": "Task name", "priority": "high|medium|low"}`
- **update**: `{"action": "update", "id": "todo_xxx", "status": "pending|in_progress|done|cancelled"}`
- **delete**: `{"action": "delete", "id": "todo_xxx"}`
- **list**: `{"action": "list", "filter": "status"}`
- **clear**: `{"action": "clear"}`

## Example

```
Create task: {"action": "create", "content": "Implement auth", "priority": "high"}
Mark done: {"action": "update", "id": "todo_xxx", "status": "done"}
List all: {"action": "list"}
```
