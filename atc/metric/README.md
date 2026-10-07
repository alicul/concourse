# Volume streaming metrics

`concourse_volume_streaming_duration_seconds` is a Prometheus histogram for
completed calls to the volume streamer's transfer operation. It records seconds
and has only two variable labels: `route` and `status` (`success` or `error`).
The shared metric event is named `volume streaming duration`, with the same
labels and a value in seconds. The histogram and queries below are specific to
the Prometheus emitter.

| Route | Meaning |
| --- | --- |
| `p2p` | Direct P2P transfer succeeded. |
| `atc_disabled` | P2P was disabled, so the transfer used the ATC. |
| `atc_unsupported` | The source or destination did not support P2P. |
| `atc_group_mismatch` | The workers had different streaming groups. |
| `atc_fallback` | P2P setup or transfer failed, and an ATC retry was attempted. |

Each operation produces one observation, including failures. A fallback's
duration includes the failed P2P attempt and the ATC retry; its status describes
the final transfer result. An expected transfer between different groups is
therefore distinguishable from a P2P failure. Disabled P2P takes precedence over
the other routing reasons. Two empty groups still qualify for P2P.

The metric includes non-volume artifacts streamed into volumes, but excludes
single-file reads (`StreamFile`) and resource-cache initialization after transfer.
A successful transfer followed by failed cache initialization still records a
successful transfer. Existing `concourse_volumes_volumes_streamed` and
`concourse_volumes_volumes_streamed_via_fallback` counters retain their original meaning:
the former counts successful volume-source transfers, while the latter counts
ATC retries after P2P failure, even if the retry fails.

## Example queries

Transfers per second, including failures, by route and outcome (the histogram's
`_count` provides the counter, so no separate transfer counter is needed):

```promql
sum by (route, status) (
  rate(concourse_volume_streaming_duration_seconds_count[5m])
)
```

Percentage of P2P attempts that needed ATC fallback. Deliberate ATC transfers are
excluded from the denominator; with no P2P attempts this ratio is undefined.

```promql
100 * sum(rate(concourse_volume_streaming_duration_seconds_count{route="atc_fallback"}[5m]))
  / sum(rate(concourse_volume_streaming_duration_seconds_count{route=~"p2p|atc_fallback"}[5m]))
```

95th-percentile duration of successful transfers by route, in seconds:

```promql
histogram_quantile(0.95,
  sum by (le, route) (
    rate(concourse_volume_streaming_duration_seconds_bucket{status="success"}[5m])
  )
)
```

The highest finite bucket is 600 seconds, so this quantile cannot resolve
durations above ten minutes. The sum still records their full duration.

## Cardinality budget

Cardinality is the number of distinct time series. Every unique combination of
metric name and label values creates a separate series. Sending more transfers
with the same labels updates existing series instead of creating new ones.

Five routes times two statuses gives ten combinations. Each classic histogram
has nine finite buckets (0.1, 0.5, 1, 5, 10, 30, 60, 300, 600 seconds), an `+Inf`
bucket, a sum, and a count: **120 series per ATC scrape target**, before any
additional deployment labels. All ten combinations are initialized at zero.

For comparison, adding source-worker and destination-worker labels in a cluster
of 100 workers could multiply the combinations by up to 100 x 100. Adding build
IDs or volume handles would keep creating series as new builds or volumes
arrive. More series consume more memory, disk, scrape bandwidth, and query CPU;
aggregating them in a dashboard does not remove the underlying storage cost.
See the [Prometheus instrumentation guidance](https://prometheus.io/docs/practices/instrumentation/#do-not-overuse-labels).

Worker and group names belong in diagnostic logs. Group mismatches log these
fields at debug level; P2P fallback errors also include them. No worker names,
group names, build IDs, volume handles, or error strings are metric labels.
