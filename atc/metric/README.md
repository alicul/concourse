# Volume streaming metrics

Every volume transfer between workers emits one `volume streaming` metric
event set from the web node's streamer. The Prometheus emitter turns it into
the families below; the other emitters receive the same events with the same
attributes as tags. Worker and streaming group names are the only free-form
values, and they are bounded as described under *Cardinality*.

| Family | Type | Labels | What it measures |
| --- | --- | --- | --- |
| `concourse_volume_streaming_duration_seconds` | histogram | `route`, `status` | Seconds per completed transfer operation, including a failed P2P attempt before a fallback. |
| `concourse_volume_streaming_transfers_total` | counter | `route`, `status`, `src_group`, `dst_group` | Completed transfer operations. Same count basis as the histogram's `_count`. |
| `concourse_volume_streaming_bytes_total` | counter | `route`, `status`, `src_group`, `dst_group` | Compressed bytes moved on the route that completed the operation. |
| `concourse_volume_streaming_unmeasured_transfers_total` | counter | `route` | Transfers whose byte count was not reported (see *Byte counts*). |
| `concourse_volume_streaming_worker_bytes_total` | counter | `worker`, `direction` | Compressed bytes each worker `sent` or `received`. |
| `concourse_volume_streaming_worker_transfers_total` | counter | `worker`, `direction`, `status` | Transfers each worker took part in, by side and outcome. |

## Labels

| Label | Values |
| --- | --- |
| `route` | `p2p`: direct worker-to-worker transfer succeeded. `atc_disabled`: P2P streaming is off, relayed through the web node. `atc_unsupported`: source or destination cannot stream P2P, relayed. `atc_group_mismatch`: the workers have different streaming groups, relayed. `atc_fallback`: the P2P attempt failed and the relay retry was made. |
| `status` | `success` or `error`, the outcome of the whole operation. A fallback's status is the retry's. |
| `src_group`, `dst_group` | The workers' `--p2p-streaming-group` names. `ungrouped` for a worker without a group, `unknown` for a source that is not a worker volume, `other` once the cap below is reached. |
| `worker` | A single worker name. The source and destination of one transfer are separate series, never a pair. |
| `direction` | `sent` for the source worker, `received` for the destination. |

Routing follows from the groups: `p2p` and `atc_fallback` only occur when
both workers share a group, `atc_group_mismatch` only when they differ, so the
group pair of an `atc_group_mismatch` series is cross-zone traffic through the
web node by construction. P2P streaming disabled takes precedence over the
other reasons. Two ungrouped workers still qualify for P2P.

Single-file reads (`StreamFile`) and resource-cache registration after a
transfer are not measured. A transfer that succeeds but whose cache
registration fails is still recorded as a success. The pre-existing
`concourse_volumes_volumes_streamed` and `..._via_fallback` counters keep
their meaning: successful volume-source transfers, and relay retries after a
P2P failure even when the retry fails.

## Byte counts

Bytes are the compressed tar stream as sent, in the configured streaming
compression, excluding HTTP framing. On relayed routes the web node counts
what it forwards, so the count is exact for any worker version, and a failed
transfer records the bytes relayed before the failure under `status="error"`.
On the `p2p` route the web node never sees the data: the source worker counts
what it sends and reports it on its `stream-p2p-out` response as the
`X-Baggageclaim-Streamed-Bytes` HTTP trailer. The body of that response is
unchanged, so a web node that predates the trailer ignores it, and a worker
that predates it sends none.

A transfer without a reported count increments
`concourse_volume_streaming_unmeasured_transfers_total{route="p2p"}` instead
of the bytes counter and adds zero to the per-worker bytes. During a rolling
upgrade this family shows how much of the P2P byte total is missing; it stays
at zero once every worker runs a build with the trailer.

A fallback only accounts the relay retry's bytes, so `route=~"atc_.*"` always
means bytes that crossed the web node. The bytes of the failed P2P attempt are
logged on the fallback error line (`p2p-bytes-sent`).

## Where the worker pair is

The exact source-to-destination pair of each transfer is deliberately not a
metric label (see *Cardinality*). It is recorded three times:

- the build's `streaming volume` event, visible in the build log and UI;
- the streamer's `stream.end` log line, with `route`, `bytes`, `duration`
  and both groups alongside the existing `from` and `to` worker names;
- the `volume.P2pStreamOut` and `volume.StreamThroughATC` trace spans, with
  `origin-worker`, `dest-worker`, `route` and `bytes` attributes when tracing
  is configured.

## Example queries

Throughput per group pair and route, in bytes per second:

```promql
sum by (src_group, dst_group, route) (rate(concourse_volume_streaming_bytes_total[5m]))
```

Bytes relayed through web nodes, i.e. everything that did not go direct:

```promql
sum(rate(concourse_volume_streaming_bytes_total{route=~"atc_.*"}[5m]))
```

Cross-zone traffic matrix, the first place to look for a misconfigured group:

```promql
sum by (src_group, dst_group) (increase(concourse_volume_streaming_transfers_total{route="atc_group_mismatch"}[1h]))
```

Share of P2P attempts that needed the fallback, per source group. Deliberate
relays are excluded from the denominator; with no P2P attempts the ratio is
undefined.

```promql
sum by (src_group) (rate(concourse_volume_streaming_transfers_total{route="atc_fallback"}[5m]))
  / sum by (src_group) (rate(concourse_volume_streaming_transfers_total{route=~"p2p|atc_fallback"}[5m]))
```

Hottest senders and receivers, and the per-worker direct-send failure rate:

```promql
topk(10, sum by (worker) (rate(concourse_volume_streaming_worker_bytes_total{direction="sent"}[1h])))
topk(10, sum by (worker) (rate(concourse_volume_streaming_worker_bytes_total{direction="received"}[1h])))
sum by (worker) (rate(concourse_volume_streaming_worker_transfers_total{direction="sent",status="error"}[15m]))
  / sum by (worker) (rate(concourse_volume_streaming_worker_transfers_total{direction="sent"}[15m]))
```

Mean compressed transfer size per route, and the fraction of transfers still
without a byte count:

```promql
sum by (route) (rate(concourse_volume_streaming_bytes_total[1h]))
  / sum by (route) (rate(concourse_volume_streaming_transfers_total[1h]))
sum(rate(concourse_volume_streaming_unmeasured_transfers_total[5m]))
  / sum(rate(concourse_volume_streaming_transfers_total[5m]))
```

95th-percentile duration of successful transfers by route, in seconds. The
highest finite bucket is 600 seconds, so this quantile cannot resolve
durations above ten minutes; the sum still records them in full.

```promql
histogram_quantile(0.95,
  sum by (le, route) (
    rate(concourse_volume_streaming_duration_seconds_bucket{status="success"}[5m])
  )
)
```

Live series budget of this feature on one web node:

```promql
count({__name__=~"concourse_volume_streaming_.*"})
```

## Cardinality

Cardinality is the number of distinct time series. Every unique combination
of metric name and label values is a series; repeated transfers with the same
labels update existing series. A classic histogram costs its buckets plus
`+Inf`, `_sum` and `_count` per label set, twelve series here; a counter costs
one. That is why groups go on the counters and the histogram keeps only
`route` and `status`.

Let `G'` be the number of distinct group label values including the
`ungrouped` sentinel, and `W` the number of workers that streamed.

| Family | Series |
| --- | --- |
| duration histogram | 10 label sets x 12 = 120, fixed |
| transfers and bytes counters | at most 6G'^2 + G' each, since the routes constrain which group pairs can appear; 10 series each pre-initialised for the ungrouped pair |
| unmeasured counter | 5, fixed |
| per-worker bytes | 2W |
| per-worker transfers | 4W |

| Fleet | Worst case for this feature | `{src_worker,dst_worker}` labels instead, per counter |
| --- | --- | --- |
| 20 workers, 2 groups | 359 | 3,800 |
| 100 workers, 5 groups | 1,169 | 99,000 |
| 500 workers, 20 groups | 8,459 | 2,495,000 |

Three controls keep the bound in the operator's hands:

- The Prometheus emitter accepts only the listed `route`, `status` and
  `direction` values and drops events with anything else, so a caller cannot
  mint series by accident. Negative values are dropped too, because adding
  them to a counter panics.
- Distinct group values are capped by `--prometheus-volume-streaming-max-groups`
  (default 32). Groups are network zones, so a few dozen is generous; a
  deployment that names a group per worker would otherwise turn `G'` into `W`.
  Values past the cap are labelled `other` and an error is logged once.
- Per-worker series are deleted once a worker leaves the workers table, using
  the web node's worker cache, not after a period of inactivity. Heartbeat
  events only reach the web node a worker's TSA forwards to, and deleting a
  live worker's counter would reset it and lose the next increment.

No build IDs, pipeline, job or step names, volume handles or error strings are
labels; those belong in logs, build events and traces. The other emitters copy
every attribute of an event as a tag, which is why the attribute set itself is
bounded rather than only the Prometheus label set. New Relic forwards a fixed
list of event names and does not receive these events unless that list is
extended.
