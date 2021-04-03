This is a fork of [Thanos](https://github.com/thanos-io/thanos) with some patches for `receive` component denied in upstream:
 - Receive: quorum for 2 nodes is `1` [#3231](https://github.com/thanos-io/thanos/pull/3231) 
 - Receive: Ability to populate tenant_id from metric labels (`--receive.extract-labels-config`), see [below](#why-extract-tenants-from-metrics)
 - Receive: fix for broken mmap file startup error
 - Receive: fix for compaction failed due to `invalid block sequence: block time ranges overlap` 

Compiled images available at: https://hub.docker.com/repository/docker/sepa/thanos/tags

## Why Extract Tenants from Metrics?

Consider you have 2 prometheuses in different locations configured like so:
```yaml
# first
global:
  external_labels:
    prometheus: A
    location: dc1

# second
global:
  external_labels:
    prometheus: B
    location: dc2
```
And you want to have all metrics available in one single place, let's say `dc3`. There are two ways to solve this:

### Pull based

Classical one is per https://github.com/thanos-io/thanos#architecture-overview, you are adding `thanos-sidecar` component to each prometheus. 
- For current metrics `thanos-query` connects to `thanos-sidecar` and pulls the data right from prometheus remote-read API. 
- Data older than 2h is uploaded to S3 and available via `thanos-store` component like so:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HAXEM | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | sidecar |
| 0WTQQF | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | sidecar |
```
So, on S3 side each of your prometheus data is available independently and marked by same labels. You can use `store` [sharding based on labels](https://thanos.io/tip/components/store.md/#external-label-partitioning-sharding) to scale queries. Or you can drop only `prometheus=A` data, and leave `prometheus=B` to be available. 

Cool! But what to do if you do not have connectivity from `thanos-query` directly to you prometheuses (i.e. NAT)?

### Push based

[Receive](https://thanos.io/tip/components/receive.md/#receiver) component was created to solve the problem for "air-gapped, or egress only environments". We're setting up `receive` in `dc3`, and targeting `remote_write` section of each prometheus to push metrics to `dc3`. 
In this case `receive` writes data to S3 like this:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HEYSD | 01-01-2021 00:00:00 | 2h    | receive=D,location=dc3    | receive |
```
Now we have one large block on S3 side, with cumulative data from all the prometheuses. There are no labels to use for `store` sharding above.  
Also, what happens if `receive` would be down for some time? By default prometheus writes 2h blocks, so if downtime would be longer - you would loose the data. But data is still on prometheus disk. Let's add `sidecar` component and upload it to S3:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HEYSD | 01-01-2021 00:00:00 | 2h    | receive=D,location=dc3    | receive |
| 0HAXEM | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | sidecar |
| 0WTQQF | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | sidecar |
```
And now we have duplicated data on graphs! This of course could be fixed by removing label `location=dc3` from receive:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HEYSD | 01-01-2021 00:00:00 | 2h    | receive=D                 | receive |
| 0HAXEM | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | sidecar |
| 0WTQQF | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | sidecar |
```
Ok, now `query` is able to de-duplicate the data, and graphs are back to normal. We're able to see the data at time when `receive` was down, but still have space wasted on S3 side. Yes, blobs have identical data, but `compactor` would not be able to dedup it, because all blocks have different labels.

Wouldn't it be great if we could push metrics via `receive` in a way compatible with `sidecar` uploads?

### Tenants

`Receive` has ability to receive metrics to separate TSDB based on HTTP header (`THANOS-TENANT` by default). We can configure such header on sending side with value of `prometheus` label. And then configure `receive` to assign value back to this label via `--receive.tenant-label-name="prometheus"`.
This way we could get:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HEYSD | 01-01-2021 00:00:00 | 2h    | prometheus=A              | receive |
| 0GWRSD | 01-01-2021 00:00:00 | 2h    | prometheus=B              | receive |
| 0HAXEM | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | sidecar |
| 0WTQQF | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | sidecar |
```
Better! Now we could use `store` sharding, but data still would not be compacted.

### Extract Tenants from Metrics

The patch adds new argument:
```
$ docker run sepa/thanos:v0.17.2 receive -h
...
      --receive.tenant-label-name="tenant_id"
                                 Label name through which the tenant will be
                                 announced.
      --receive.extract-labels-config-file=<file-path>
                                 Path to YAML config for external_labels
                                 extraction from received metrics. Also enables
                                 tenant extraction from label set in
                                 receive.tenant-label-name
      --receive.extract-labels-config=<content>
                                 Alternative to
                                 'receive.extract-labels-config-file' flag
                                 (lower priority). Content of YAML config for
                                 external_labels extraction from received
                                 metrics. Also enables tenant extraction from
                                 label set in receive.tenant-label-name
...
```
Let's start `receive` like so:
```yaml
args:
  - receive
  - --receive.tenant-label-name=prometheus
  - --receive.extract-labels-config={defaultExternalLabels:[location]}
```
Which means:
- Extract label `prometheus` from received metrics to distinguish Tenants. Value of this label should be globally unique.
- Extract label `location` from received metrics to be `external_label`

That would lead to such blocks on S3:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |   SRC   |
|--------|---------------------|-------|---------------------------|---------|
| 0HEYSD | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | receive |
| 0GWRSD | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | receive |
| 0HAXEM | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | sidecar |
| 0WTQQF | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | sidecar |
```
And after some time this would be compacted to:
```bash
# thanos tools bucket inspect
|  ULID  |        FROM         | RANGE |          LABELS           |    SRC    |
|--------|---------------------|-------|---------------------------|-----------|
| 0UYGSB | 01-01-2021 00:00:00 | 2h    | prometheus=A,location=dc1 | compactor |
| 0UGVER | 01-01-2021 00:00:00 | 2h    | prometheus=B,location=dc2 | compactor |
```

### extract-labels-config

Full example for extract-labels-config:
```yaml
defaultExternalLabels:
- location
- env
tenantExternalLabels:
  kube-b:
  - location
  kube-c:
  - location
  - env 
  - site
```
Where:
 - `defaultExternalLabels` list of labels to extract to `external_labels`
 - `tenantExternalLabels` list of overrides if needed, per-tenant.

Given the metrics:
```ini
metric{prometheus="kube-a", location="dc1", env="dev", instance="a"} 1
metric{prometheus="kube-b", location="dc2", env="dev", instance="b"} 1
metric{prometheus="kube-c", location="dc3", env="dev", instance="c"} 1
```
and config above would lead to such TSDB blocks:
```ini
/kube-a
# block labels: {prometheus="kube-a", location="dc1", env="dev"}
metric{instance="a"} 1

/kube-b
# block labels: {prometheus="kube-b", location="dc2"}
metric{env="dev", instance="b"} 1

/kube-c
# block labels: {prometheus="kube-c", location="dc3", env="dev"}
metric{instance="c"} 1
```

So, now you can push metrics via both "`sidecar`>S3", and "`remote_write`>`receive`" for better availability. These metrics would be correctly deduplicated on S3 side. And produced blobs-per-tenant are better suited for `store` sharding.   
Obviously this only makes sense in trusted environments, where you control both Thanos and Prometheus sides.