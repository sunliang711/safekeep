variable "image" {
  type    = string
  default = "sunliang711/safekeep:v0.0.1"
}

job "safekeep" {
  datacenters = ["dc1"]
  type        = "service"

  group "server" {
    count = 1

    network {
      port "http" {
        static = 8333
        to     = 8333
      }
    }

    service {
      name     = "safekeep"
      provider = "nomad"
      port     = "http"

      check {
        name     = "liveness"
        type     = "http"
        path     = "/healthz"
        interval = "10s"
        timeout  = "2s"
      }

      check {
        name     = "readiness"
        type     = "http"
        path     = "/readyz"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "safekeep" {
      driver = "docker"

      # 环境变量优先级高于容器内置 config.toml 与数据库默认配置（仅命令行参数优先级更高）。
      env {
        # --- 基础服务与持久化路径 ---
        SAFEKEEP_CONFIG          = "/app/config.toml"
        SAFEKEEP_LISTEN          = ":8333"
        SAFEKEEP_DATA_DIR        = "/data"
        SAFEKEEP_SPOOL_DIR       = "/data/spool"
        # 对外访问基准地址（用于 Dropbox OAuth2 回调与同源校验；局域网/反代部署请改为实际访问地址）
        SAFEKEEP_PUBLIC_BASE_URL = "http://localhost:8333"
        # 可选：固定 32 字节加密主密钥（64 位十六进制或 Base64）；不设置时自动生成并持久化到 /data/master.key
        # SAFEKEEP_MASTER_KEY    = ""
        # 可选：额外允许跨域的 Origin 白名单（多个用英文逗号分隔）
        # SAFEKEEP_CORS_ORIGINS  = "http://192.168.1.10:8333"

        # 注：上传限额（默认 100 MiB）、每日流量熔断（默认 1 GiB）、缓冲总容量上限与任务调度参数
        #（Worker 并发数、同步超时、最大重试次数）统一持久化在 SQLite 数据库，请在 Web 管理端「系统设置 -> 全局运行参数」中配置。

        # --- 可选：SMTP 邮件告警（也可直接在 Web 管理端「系统设置 -> SMTP 邮件告警」配置）---
        # SAFEKEEP_SMTP_ENABLED    = "true"
        # SAFEKEEP_SMTP_HOST       = "smtp.qq.com"
        # SAFEKEEP_SMTP_PORT       = "465"
        # SAFEKEEP_SMTP_TLS_MODE   = "tls" # starttls | tls | none
        # SAFEKEEP_SMTP_USERNAME   = "alerts@example.com"
        # SAFEKEEP_SMTP_PASSWORD   = "your_smtp_auth_code"
        # SAFEKEEP_SMTP_FROM_EMAIL = "alerts@example.com"
        # SAFEKEEP_SMTP_FROM_NAME  = "SafeKeep"
        # SAFEKEEP_SMTP_TO_EMAILS  = "ops@example.com,admin@example.com"
      }

      config {
        image = var.image
        ports = ["http"]
        # 生产环境建议挂载宿主机目录持久化 SQLite、master.key 与缓冲盘：
        # volumes = [
        #   "/opt/safekeep/data:/data",
        # ]
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
