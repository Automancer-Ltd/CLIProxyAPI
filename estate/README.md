# Estate model catalog

`models.json` is the general model catalog every estate hub loads (`models.catalog` in the hub config points at
this file's raw GitHub URL). It is upstream's live catalog (`router-for-me/models` main) plus models upstream has
not listed yet. When upstream lists them, set `models.catalog` back to `""` so the hubs follow upstream again.

- 9 Nov 2026 (Fable): added `muse-ember-1.0` (Meta released it that day; Waseem: cheap work goes to Muse Ember).
  Context 1M, output 512K, inputs text/image/audio/video from Meta's model page; thinking levels copied from
  `muse-spark-1.3`, not confirmed.

`devin_models.json` is the Devin catalog the estate hubs load (`models.devin-catalog` points at this file's raw
GitHub URL). It is upstream's live Devin catalog (`router-for-me/models` main, `devin_models.json`) plus Devin
models upstream has not listed yet. A hub left on `devin-catalog: ""` refreshes from upstream every 3 hours and
drops anything only the embedded copy carries, so an estate addition must live here. When upstream lists the
model, set `models.devin-catalog` back to `""` and delete this file.

- 24 Nov 2026 (t17486): added `swe-2-1` (Cognition released SWE-2.1 that day; Waseem: it replaces SWE-2 and is
  the first choice in its tier while free). Context 262,144 and output 65,536, efforts low/medium/high/xhigh/max,
  all from `devin models list --format json` on m6 (CLI 3000.14.30821). Text input only: image input is not
  confirmed. SWE-2.1 Lightning (`swe-2-1-*-lightning`, $5/$25 per million tokens on Pro) is left out on purpose so
  no lane reaches a paid variant.
