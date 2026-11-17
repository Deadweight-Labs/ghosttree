---
description: Prüft nach dem Merge, ob das Wissen aus dem Diff in ghosttree steht, und schlägt fehlende Einträge vor
on: pr.merged
uses: ghosttree/pr-knowledge@v1
results:
  knowledge: 5
model: mittel
budget:
  run_usd: 0.30
  month_usd: 5
---
