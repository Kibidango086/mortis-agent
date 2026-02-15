# Agents

7 built-in agents for different tasks.

## Agents

| Agent | Temp | Tools | Use For |
|-------|------|-------|---------|
| general | 0.7 | all | Daily tasks |
| build | 0.3 | file+shell+search | Coding |
| plan | 0.5 | read+search+todo | Architecture |
| explore | 0.6 | search+web | Research |
| debug | 0.2 | file+shell+search | Debugging |
| review | 0.4 | read+search | Code review |
| doc | 0.5 | file | Documentation |

## Usage

```bash
# Use specific agent
mortisagent agent --agent build -m "Implement feature"

# Or during chat
switch to build agent
```

## Custom Agent

Copy `agents/general.json`, modify, save as `agents/myagent.json`
