---
name: tmux
description: Control tmux sessions
---

# Tmux

Control tmux for interactive sessions.

## Quick Start

```bash
# Create session
tmux new -d -s mysession

# Send command
tmux send-keys -t mysession 'ls' Enter

# Capture output
tmux capture-pane -p -t mysession

# Kill session
tmux kill-session -t mysession
```
