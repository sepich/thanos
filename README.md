This is a fork of [Thanos](https://github.com/thanos-io/thanos) with some patches for `receive` component:
- Receive: quorum for 2 nodes is `1` [#3231](https://github.com/thanos-io/thanos/pull/3231)
- Receive: make `__meta_tenant_id` available at `--receive.relabel-config` phase. You can read from it to validate other labels. And you can write to it, to split by label:
```yaml
  - |
    --receive.relabel-config=
    - source_labels: [__meta_tenant_id, prometheus]
      regex: (foo;compose-test-1|bar;compose-test-2)
      action: keep
    - source_labels: [prometheus]
      target_label: __meta_tenant_id
```

Compiled images available at: https://hub.docker.com/repository/docker/sepa/thanos/tags
