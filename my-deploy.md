# my-deploy

本文记录我自己构建 `new-api` 镜像并通过 `docker compose` 发布的步骤，以及后续只更新 `new-api` 镜像、不重新创建 `postgres` 和 `redis` 容器的做法。

## 1. 前提

- 部署方式：`docker compose`
- 当前编排文件：`docker-compose.yml`
- 当前服务名：
  - `new-api`
  - `postgres`
  - `redis`
- 当前 `new-api` 镜像标签示例：`new-api:release20260705`

建议先确认：

```bash
docker compose version
docker version
```

## 2. 首次部署

### 2.1 准备代码

```bash
git clone git@github.com:holy-tiger/new-api.git
cd new-api
```

如果后续仓库地址发生变化，这里同步改成新的项目地址。

### 2.2 检查并修改 `docker-compose.yml`

重点确认以下内容：

- `services.new-api.image` 是否是你要发布的镜像标签
- `SQL_DSN` 是否指向 `postgres`
- `REDIS_CONN_STRING` 是否指向 `redis`
- `postgres` 和 `redis` 的密码是否已经改成生产值
- `./data:/data` 和 `./logs:/app/logs` 是否符合你的目录规划

当前配置里，PostgreSQL 数据卷是命名卷 `pg_data`，只要不执行 `docker compose down -v`，数据库数据会保留。

### 2.3 构建自己的镜像

示例标签：

```bash
docker build -t new-api:release20260705 .
```

如果构建机内存紧张，建议提前加 swap。之前已经遇到过一次前端打包 OOM，现象是 `exit code 137` 和 `Killed process (node)`。

可选检查：

```bash
docker images | grep new-api
```

### 2.4 首次启动

```bash
docker compose up -d
```

首次启动会创建：

- `new-api` 容器
- `postgres` 容器
- `redis` 容器
- `pg_data` 卷
- `new-api-network` 网络

### 2.5 检查运行状态

```bash
docker compose ps
docker compose logs -f new-api
```

健康检查接口：

```bash
curl http://127.0.0.1:3000/api/status
```

## 3. 后续发布：只更新 `new-api` 镜像

目标是：

- 保留已有 `postgres` 容器
- 保留已有 `redis` 容器
- 保留 PostgreSQL 数据卷
- 只替换 `new-api` 容器

### 3.1 构建新镜像

每次发布使用一个新的标签，避免混淆旧镜像：

```bash
docker build -t new-api:release20260706 .
```

### 3.2 修改 `docker-compose.yml` 中的镜像标签

把：

```yaml
image: new-api:release20260705
```

改成：

```yaml
image: new-api:release20260706
```

### 3.3 只更新 `new-api` 服务

执行：

```bash
docker compose up -d new-api
```

这个命令会：

- 重新创建 `new-api` 容器
- 使用新的镜像标签
- 保持 `postgres` 和 `redis` 不变

它不会删除 `postgres`、`redis`，也不会清空 `pg_data`。

### 3.3.1 重要：必须在原来的 compose 项目里更新

`docker compose` 默认会把当前目录名当成 project name。

例如在目录 `new-api-github` 下执行：

```bash
docker compose up -d new-api
```

Compose 可能会尝试创建一套新的资源：

- `new-api-github_new-api-network`
- `new-api-github_pg_data`

但如果 `docker-compose.yml` 里写死了：

```yaml
container_name: new-api
container_name: postgres
container_name: redis
```

而机器上已经存在旧的 `new-api`、`postgres`、`redis` 容器，就会报容器名冲突，例如：

```text
Error response from daemon: Conflict. The container name "/redis" is already in use
```

这不是镜像问题，而是你在“新的 compose 项目”里试图创建和旧环境同名的容器。

正确做法：

1. 回到原来部署这套服务时使用的目录，再执行：

```bash
docker compose up -d new-api
```

2. 或者在任意目录显式指定原来的 project name：

```bash
docker compose -p <原项目名> up -d new-api
```

先确认现有 compose 项目：

```bash
docker compose ls
```

查看现有容器：

```bash
docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
```

如果你的目标是“只更新已有的 `new-api`”，不要在新的目录名下直接运行默认的 `docker compose up -d new-api`。

### 3.4 更新后检查

```bash
docker compose ps
docker compose logs -f new-api
curl http://127.0.0.1:3000/api/status
```

也可以额外确认 `postgres` 和 `redis` 没被重建：

```bash
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
```

## 4. 如果镜像是先构建、再推送到镜像仓库

如果不是在部署机本地构建，而是先推到私有仓库或 Docker Hub，流程改成：

### 4.1 构建并打标签

```bash
docker build -t registry.example.com/my/new-api:release20260706 .
```

### 4.2 推送镜像

```bash
docker push registry.example.com/my/new-api:release20260706
```

### 4.3 在部署机更新 `docker-compose.yml`

把 `image` 改成远端仓库地址：

```yaml
image: registry.example.com/my/new-api:release20260706
```

### 4.4 拉取并只更新 `new-api`

```bash
docker compose pull new-api
docker compose up -d new-api
```

## 5. 常用命令

查看所有服务状态：

```bash
docker compose ps
```

只看 `new-api` 日志：

```bash
docker compose logs -f new-api
```

重启 `new-api`：

```bash
docker compose restart new-api
```

停止全部服务：

```bash
docker compose stop
```

启动全部服务：

```bash
docker compose up -d
```

## 6. 不要这样做

以下操作会影响数据库或缓存生命周期，发布时不要随手执行：

```bash
docker compose down
docker compose down -v
docker compose up -d --force-recreate
```

说明：

- `docker compose down` 会删除容器和网络
- `docker compose down -v` 还会删除卷，数据库数据也可能一起丢失
- `docker compose up -d --force-recreate` 会强制重建所有服务，不适合“只更新 `new-api`”的场景

如果只是发新版，请坚持使用：

```bash
docker compose up -d new-api
```

## 7. 回滚

如果新版本有问题：

1. 把 `docker-compose.yml` 里的 `image` 改回上一个可用标签
2. 执行下面命令重新部署 `new-api`

```bash
docker compose up -d new-api
```

如果旧镜像还在本地，回滚会很快完成。

## 8. 这次踩过的坑

构建镜像时，前端 `vite build` 可能吃掉大量内存。在内存 8G 左右、且没有 swap 的机器上，容易出现：

- `Killed`
- `exit code: 137`
- `Out of memory: Killed process (node)`

解决方式：

- 给宿主机增加 swap
- 再重新执行 `docker build`

排查命令：

```bash
dmesg -T | grep -i -E 'killed process|out of memory|oom'
free -h
```
