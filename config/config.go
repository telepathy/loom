// Package config 加载 Loom 的所有配置项（环境变量驱动）。
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config 是 Loom 的完整配置结构体。所有字段从环境变量读取，提供合理的默认值。
type Config struct {
	Port             int
	K8sNamespace     string
	GitImage         string
	JDKImagePrefix   string
	DefaultJDK       string
	MaxParallel      int
	JobTimeout       int
	PlanTTL          int
	ConfigmapName    string
	SSHSecretName    string
	LocalMode        bool
	MySQLDSN         string
	AkashaAPIURL     string   // akasha gradle.properties API URL
	ImagePullSecrets []string // imagePullSecrets 名称列表
	InCluster        bool
}

// Load 从环境变量读取配置，缺失时使用默认值。
func Load() *Config {
	return &Config{
		Port:             getEnvInt("DAS_PORT", 8080),
		K8sNamespace:     getEnv("DAS_K8S_NAMESPACE", "gps"),
		GitImage:         getEnv("DAS_GIT_IMAGE", "registry/gps-das-git:latest"),
		JDKImagePrefix:   getEnv("DAS_JDK_IMAGE_PREFIX", "registry/gps-das-jdk"),
		DefaultJDK:       getEnv("DAS_DEFAULT_JDK", "17"),
		MaxParallel:      getEnvInt("DAS_MAX_PARALLEL", 5),
		JobTimeout:       getEnvInt("DAS_JOB_TIMEOUT", 600),
		PlanTTL:          getEnvInt("DAS_PLAN_TTL", 3600),
		ConfigmapName:    getEnv("DAS_CONFIGMAP_NAME", "das-init-script"),
		SSHSecretName:    getEnv("DAS_SSH_SECRET_NAME", "codeup-ssh"),
		LocalMode:        getEnv("DAS_LOCAL_MODE", "false") == "true",
		MySQLDSN:         getEnv("DAS_MYSQL_DSN", ""),
		AkashaAPIURL:     getEnv("DAS_AKASHA_API_URL", ""),
		ImagePullSecrets: parseCommaSep(getEnv("DAS_IMAGE_PULL_SECRETS", "")),
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

func parseCommaSep(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
