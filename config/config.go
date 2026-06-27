// Package config 加载 Loom 的所有配置项（环境变量驱动）。
package config

import (
	"os"
	"strconv"
)

// Config 是 Loom 的完整配置结构体。所有字段从环境变量读取，提供合理的默认值。
//
// 环境变量对照表（来自 das_design.md §7.2）：
//
//	变量                     默认值                          说明
//	DAS_PORT                 8080                            HTTP 监听端口
//	DAS_K8S_NAMESPACE        gps                             Job 创建的 namespace
//	DAS_GIT_IMAGE            registry/gps-das-git:latest      initContainer clone 镜像
//	DAS_JDK_IMAGE_PREFIX     registry/gps-das-jdk             analyze 镜像前缀，拼 :<jdk>
//	DAS_DEFAULT_JDK          17                              未指定 jdk 时的默认版本
//	DAS_MAX_PARALLEL         5                               同时创建的 Job 数上限
//	DAS_JOB_TIMEOUT          600                             单 Job 超时秒数（activeDeadlineSeconds）
//	DAS_PLAN_TTL             3600                            计划结果在内存中保留的秒数
//	DAS_CONFIGMAP_NAME       das-init-script                 init script ConfigMap 名称
//	DAS_SSH_SECRET_NAME      codeup-ssh                      git ssh 私钥 Secret 名称
//	DAS_LOCAL_MODE           false                           本地开发模式（跳过 K8s，直接 exec gradlew）
//	DAS_MYSQL_DSN            —                                MySQL 连接串（设置后启用自驱动分析，可直接读 gps_repos 表）
//	DAS_AKASHA_API_URL       —                                akasha gradle.properties API 完整 URL
type Config struct {
	Port            int
	K8sNamespace    string
	GitImage        string
	JDKImagePrefix  string
	DefaultJDK      string
	MaxParallel     int
	JobTimeout      int // 秒
	PlanTTL         int // 秒
	ConfigmapName   string
	SSHSecretName   string
	LocalMode       bool
	MySQLDSN        string
	AkashaAPIURL    string // akasha gradle.properties API 完整 URL（如 http://akasha:8080/api/v1/gradle-properties）
	// InCluster 指示是否在 K8s 集群内运行（影响 client-go 初始化方式）。
	// 非环境变量字段，由 main.go 根据运行环境自动判断。
	InCluster bool
}

// Load 从环境变量读取配置，缺失时使用默认值。
func Load() *Config {
	return &Config{
		Port:           getEnvInt("DAS_PORT", 8080),
		K8sNamespace:   getEnv("DAS_K8S_NAMESPACE", "gps"),
		GitImage:       getEnv("DAS_GIT_IMAGE", "registry/gps-das-git:latest"),
		JDKImagePrefix: getEnv("DAS_JDK_IMAGE_PREFIX", "registry/gps-das-jdk"),
		DefaultJDK:     getEnv("DAS_DEFAULT_JDK", "17"),
		MaxParallel:    getEnvInt("DAS_MAX_PARALLEL", 5),
		JobTimeout:     getEnvInt("DAS_JOB_TIMEOUT", 600),
		PlanTTL:        getEnvInt("DAS_PLAN_TTL", 3600),
		ConfigmapName:  getEnv("DAS_CONFIGMAP_NAME", "das-init-script"),
		SSHSecretName:  getEnv("DAS_SSH_SECRET_NAME", "codeup-ssh"),
		LocalMode:      getEnv("DAS_LOCAL_MODE", "false") == "true",
		MySQLDSN:       getEnv("DAS_MYSQL_DSN", ""),
			AkashaAPIURL:   getEnv("DAS_AKASHA_API_URL", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
