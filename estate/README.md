# Estate model catalog

`models.json` is the general model catalog every estate hub loads (`models.catalog` in the hub config points at
this file's raw GitHub URL). It is upstream's live catalog (`router-for-me/models` main) plus models upstream has
not listed yet. When upstream lists them, set `models.catalog` back to `""` so the hubs follow upstream again.

- 9 Nov 2026 (Fable): added `muse-ember-1.0` (Meta released it that day; Waseem: cheap work goes to Muse Ember).
  Context 1M, output 512K, inputs text/image/audio/video from Meta's model page; thinking levels copied from
  `muse-spark-1.3`, not confirmed.
