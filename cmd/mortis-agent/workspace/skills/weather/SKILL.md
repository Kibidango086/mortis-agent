---
name: weather
description: Weather info, no API key needed
---

# Weather

## wttr.in

```bash
# Current
wttr.in/London?format=3

# Full
wttr.in/London?T
```

## Open-Meteo

```bash
curl "https://api.open-meteo.com/v1/forecast?latitude=51.5&longitude=-0.12&current_weather=true"
```
