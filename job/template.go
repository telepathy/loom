package job

import (
	"bytes"
	"text/template"
)

// JobTemplateData 是 K8s Job YAML 模板的渲染参数。
//
// 模板字段说明（来自 das_design.md §4.3）：
//   - RepoID, PlanID: 用于 Job 名称 das-{{repo_id}}-{{plan_id}} 和标签
//   - RepoURL: Git SSH 克隆地址
//   - Tag: 本次发布的 tag（git clone --branch {{tag}}），GPS 驱动模式
//   - Branch: 发布分支（git clone --branch {{branch}}），自驱动模式优先
//   - JDK: JDK 大版本，决定 analyze 镜像 tag
//   - AkashaBranch: akasha 依赖分支（-PdepBranch=$AKASHA_BRANCH）
//   - CallbackBaseURL: DAS 自身回调地址，Job 内 curl 回传用
//   - GitImage: initContainer 镜像
//   - JDKImagePrefix: analyze 镜像前缀（registry/gps-das-jdk）
//   - Namespace: K8s namespace
//   - ConfigmapName: init script ConfigMap 名称
//   - SSHSecretName: SSH 私钥 Secret 名称
//   - JobTimeout: activeDeadlineSeconds
type JobTemplateData struct {
	RepoID          string
	PlanID          string
	RepoURL         string
	Tag             string
	Branch          string // 发布分支名，自驱动模式使用；Tag 和 Branch 至少有一个
	JDK             string
	AkashaBranch    string
	CallbackBaseURL string

	// 基础设施配置
	GitImage       string
	JDKImagePrefix string
	Namespace      string
	ConfigmapName  string
	SSHSecretName  string
	JobTimeout     int
	AkashaAPIURL      string // akasha gradle.properties API URL，空则跳过
	ImagePullSecrets   []string // imagePullSecrets 名称列表
}

// jobYAML 是 K8s Job 的 Go template。
// 对应 das_design.md §4.3 的 Job 模板。
const jobYAML = `
apiVersion: batch/v1
kind: Job
metadata:
  name: das-{{.RepoID}}-{{.PlanID}}
  namespace: {{.Namespace}}
  labels:
    app: gps-das
    plan: "{{.PlanID}}"
    repo: "{{.RepoID}}"
spec:
  backoffLimit: 1
  activeDeadlineSeconds: {{.JobTimeout}}
  ttlSecondsAfterFinished: 600
  template:
    metadata:
      labels:
        app: gps-das
        plan: "{{.PlanID}}"
    spec:
      restartPolicy: Never
      volumes:
        - name: workspace
          emptyDir: {}
        - name: init-script
          configMap:
            name: {{.ConfigmapName}}
        - name: ssh-key
          secret:
            secretName: {{.SSHSecretName}}
            defaultMode: 0400
      initContainers:
        - name: clone
          image: {{.GitImage}}
          command: ["sh", "-c"]
          args:
            - |
              export GIT_SSH_COMMAND="ssh -i /keys/id_rsa -o StrictHostKeyChecking=no"
              git clone --depth 1 --branch {{if .Branch}}{{.Branch}}{{else}}{{.Tag}}{{end}} {{.RepoURL}} /work/src
              chmod -R u+w /work/src{{if .AkashaAPIURL}}
              curl -sf -o /work/src/gradle.properties '{{.AkashaAPIURL}}?depBranch={{.AkashaBranch}}' || true{{end}}
          volumeMounts:
            - name: workspace
              mountPath: /work
            - name: ssh-key
              mountPath: /keys
              readOnly: true
      containers:
        - name: analyze
          image: {{.JDKImagePrefix}}:{{.JDK}}
          workingDir: /work/src
          env:
            - name: CALLBACK_URL
              value: "{{.CallbackBaseURL}}/das/callback?plan_id={{.PlanID}}&repo_id={{.RepoID}}&tag={{.Tag}}"
          command: ["sh", "-c"]
          args:
            - |
              set -e
              ./gradlew --init-script /scripts/das.gradle \
                        help -q 2>/tmp/gradle-stderr.log || {
                ESCAPED=$(cat /tmp/gradle-stderr.log | head -200 | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')
                curl -sf -X POST "$CALLBACK_URL" \
                  -H "Content-Type: application/json" \
                  -d "{\"error\": $ESCAPED}" || true
                exit 1
              }
              curl -sf -X POST "$CALLBACK_URL" \
                -H "Content-Type: application/json" \
                --data-binary @/work/src/das-output.json
          volumeMounts:
            - name: workspace
              mountPath: /work
            - name: init-script
              mountPath: /scripts
              readOnly: true
          resources:
            requests:
              cpu: "500m"
              memory: "1Gi"
            limits:
              cpu: "2"
              memory: "2Gi"
`

var tmpl *template.Template

func init() {
	tmpl = template.Must(template.New("job").Parse(jobYAML))
}

// RenderJobYAML 渲染 K8s Job YAML。
// 将 JobTemplateData 填入模板，返回渲染后的 YAML 字符串。
func RenderJobYAML(data JobTemplateData) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
