This is fork of [Thanos](https://github.com/thanos-io/thanos) with some patches denied in upstream:
 - Receive: quorum for 2 nodes is 1 [#3231](https://github.com/thanos-io/thanos/pull/3231) 
 - Receive: Ability to populate tenant_id from metric labels (`--receive.extract-labels-config`)
 - Receive: fix for broken mmap file startup error
 - Receive: fix for compaction failed due to `invalid block sequence: block time ranges overlap` 
 
Compiled images available at: https://hub.docker.com/repository/docker/sepa/thanos/tags