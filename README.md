# gogrpcping: Measuring round trip latency with gRPC and TCP

Measures the latency of localhost and network requests with gRPC and TCP.

### Results

Running on a Google Cloud C3D AMD Genoa 4th Generation Zen 4 instance with 8 vCPUs in us-central-1. (Debian 12.5; Kernel: 6.1.0-18-cloud-amd64; /proc/cpuinfo: AMD EPYC 9B14). These instances were not created with any specific placement policy. "By default, Compute Engine places your VMs in different hosts to minimize the impact of power failures" ([official docs](https://cloud.google.com/compute/docs/instances/placement-policies-overview)).

#### Summary

* Localhost TCP: p50=7.5 µs (7-11 µs) ; Localhost gRPC: p50=49 µs (25-120 µs)
* One zone TCP: p50=39 µs (30-55 µs) ; One zone gRPC: p50=92 µs (60-140 µs)
* Across zone TCP: p50=810 µs (740-900 µs) ; Across zone gRPC: p50=860 µs (875-1000 µs)
    * NOTE: These measurements are *much* more variable. I observed up to 1.2 ms TCP latency during a ~5 minute test.

#### Localhost

```
2024/03/26 13:10:23 INFO echo measurement client=localhost_tcp measurements=10000 p1=7.2µs p25=7.41µs p50=7.48µs p75=7.55µs p90=7.67µs p95=7.73µs p99=10.18µs
2024/03/26 13:10:24 INFO echo measurement client=localhost_grpc measurements=10000 p1=26.1µs p25=40.07µs p50=49.3µs p75=57.95µs p90=66.14µs p95=72.059µs p99=110.79µs
2024/03/26 13:10:34 INFO echo measurement client=localhost_tcp measurements=10000 p1=7.44µs p25=7.62µs p50=7.68µs p75=7.73µs p90=7.77µs p95=7.85µs p99=10.91µs
2024/03/26 13:10:34 INFO echo measurement client=localhost_grpc measurements=10000 p1=25.8µs p25=39.85µs p50=48.77µs p75=57.38µs p90=65.75µs p95=71.42µs p99=98.9µs
2024/03/26 13:10:44 INFO echo measurement client=localhost_tcp measurements=10000 p1=7.45µs p25=7.57µs p50=7.63µs p75=7.68µs p90=7.73µs p95=7.8µs p99=10.63µs
2024/03/26 13:10:45 INFO echo measurement client=localhost_grpc measurements=10000 p1=26.01µs p25=40.53µs p50=49.36µs p75=57.89µs p90=66.4µs p95=71.99µs p99=106.82µs
```

#### Inside one zone

```
2024/03/26 13:16:24 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=30.29µs p25=35.67µs p50=39.08µs p75=44.18µs p90=51.62µs p95=58.551µs p99=70.051µs
2024/03/26 13:16:25 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=64.721µs p25=82.451µs p50=93.411µs p75=103.881µs p90=114.161µs p95=121.221µs p99=163.792µs
2024/03/26 13:16:36 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=31.7µs p25=35.49µs p50=37.22µs p75=39.931µs p90=43.56µs p95=45.73µs p99=49.42µs
2024/03/26 13:16:36 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=64.41µs p25=80.15µs p50=92.111µs p75=103.2µs p90=113.63µs p95=119.38µs p99=137.201µs
2024/03/26 13:16:47 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=31.22µs p25=34.36µs p50=36.92µs p75=41.36µs p90=45.29µs p95=47.67µs p99=51.771µs
2024/03/26 13:16:48 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=64.73µs p25=80.21µs p50=91.95µs p75=102.69µs p90=112.751µs p95=118.59µs p99=138.741µs
```

#### Across zones

```
2024/03/26 13:11:20 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=758.552µs p25=767.782µs p50=770.072µs p75=772.651µs p90=775.552µs p95=777.792µs p99=784.392µs
2024/03/26 13:11:29 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=832.602µs p25=849.262µs p50=856.092µs p75=864.541µs p90=880.502µs p95=898.642µs p99=920.272µs
2024/03/26 13:11:47 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=798.831µs p25=806.031µs p50=810.602µs p75=814.571µs p90=819.111µs p95=821.721µs p99=828.061µs
2024/03/26 13:11:56 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=825.032µs p25=841.941µs p50=849.212µs p75=858.531µs p90=874.641µs p95=891.631µs p99=915.862µs
2024/03/26 13:12:14 INFO echo measurement client=tcp_10.128.0.43 measurements=10000 p1=776.062µs p25=784.952µs p50=788.471µs p75=793.351µs p90=798.831µs p95=802.121µs p99=809.051µs
2024/03/26 13:12:22 INFO echo measurement client=grpc_10.128.0.43 measurements=10000 p1=774.521µs p25=789.932µs p50=796.801µs p75=805.131µs p90=820.681µs p95=839.971µs p99=862.231µs
```

