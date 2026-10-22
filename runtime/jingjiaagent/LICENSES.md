# 离线发行包许可与来源

发行包的 `manifest.json` 保留每个镜像的完整 ID、来源标签和逐组件源码身份；`source.lock.json`、`minio-source.lock.json` 与 `installer-proxy.lock.json` 保留固定来源。Office Python 与 Node 依赖在对应锁文件中固定，并随镜像安装，镜像中保留其原有许可材料。

本轮存储镜像由官方 MinIO 社区源码固定提交 `7aac2a2c5b7c882e68c1ce017d8256be2feea27f` 构建，来源为 <https://github.com/minio/minio/tree/7aac2a2c5b7c882e68c1ce017d8256be2feea27f>。原始 AGPL-3.0 许可全文保存为 `licenses/minio-AGPL-3.0.txt`，与镜像内 `/usr/share/licenses/minio/LICENSE` 相同；镜像内 `SOURCE` 保存来源地址。

其他镜像来自 manifest 所记录的官方 PostgreSQL、Redis、Nginx、ClickHouse 镜像；Agent 引擎来源与固定提交见 source lock。各组件保留各自许可，本文不替代原文。发行包不包含私有模型凭据、部署密钥、账号、业务数据库或任务文件。
