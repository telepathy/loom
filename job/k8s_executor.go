package job

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/telepathy/loom/config"
	"github.com/telepathy/loom/model"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sExecutor 通过 K8s Job 执行依赖分析。
//
// 执行流程：
//  1. initContainer clone → chmod 可写 → curl akasha API → 写入 gradle.properties
//  2. container: gradlew --init-script das.gradle help -q
//  3. Watch Job 直到完成或超时
//  4. 结果由 Job 内 curl 回调 /das/callback 写入 store + DB
type K8sExecutor struct {
	clientset *kubernetes.Clientset
	namespace string
	cfg       *config.Config
}

// NewK8sExecutor 创建 K8s 执行器。
func NewK8sExecutor(cfg *config.Config, kubeconfig string) (*K8sExecutor, error) {
	var (
		restConfig *rest.Config
		err        error
	)

	if kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		restConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("k8s clientset: %w", err)
	}

	return &K8sExecutor{
		clientset: clientset,
		namespace: cfg.K8sNamespace,
		cfg:       cfg,
	}, nil
}

// Execute 创建 K8s Job 并等待完成。
func (e *K8sExecutor) Execute(ctx context.Context, planID string, rs *model.RepoState, akashaBranch string) (*AnalysisResult, error) {
	jobName := rs.JobNameStr(planID)

	// 清理同名旧 Job（幂等重跑）
	e.deleteJobIfExists(ctx, jobName)

	ref := rs.Branch
	if ref == "" {
		ref = rs.Tag
	}
	jdk := rs.JDK
	if jdk == "" {
		jdk = e.cfg.DefaultJDK
	}

	// 创建 Job
	_, err := e.createJob(ctx, JobTemplateData{
		RepoID:          rs.RepoID,
		PlanID:          planID,
		RepoURL:         rs.RepoURL,
		Tag:             rs.Tag,
		Branch:          rs.Branch,
		JDK:             jdk,
		AkashaBranch:    akashaBranch,
		CallbackBaseURL: "http://gps-das." + e.namespace + ".svc:8080",
		GitImage:        e.cfg.GitImage,
		JDKImagePrefix:  e.cfg.JDKImagePrefix,
		Namespace:       e.namespace,
		ConfigmapName:   e.cfg.ConfigmapName,
		SSHSecretName:   e.cfg.SSHSecretName,
		GradleCachePVC:  e.cfg.GradleCachePVC,
		JobTimeout:      e.cfg.JobTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("k8s create job: %w", err)
	}

	log.Printf("[k8s] job %s created for repo %s (ref=%s, jdk=%s)", jobName, rs.RepoID, ref, jdk)

	// Watch Job 直到完成或超时
	result, err := e.waitForJob(ctx, jobName)
	if err != nil {
		e.deleteJobIfExists(context.Background(), jobName)
		return nil, err
	}

	log.Printf("[k8s] job %s completed (succeeded=%d, failed=%d)",
		jobName, result.Status.Succeeded, result.Status.Failed)

	if result.Status.Failed > 0 {
		return nil, fmt.Errorf("k8s job %s failed", jobName)
	}

	return nil, nil
}

// createJob 通过 K8s API 创建 Job。
// Job 结构：initContainer(克隆+chmod+akasha) + container(gradlew分析)。
func (e *K8sExecutor) createJob(ctx context.Context, data JobTemplateData) (*batchv1.Job, error) {
	jobName := "das-" + data.RepoID + "-" + data.PlanID

	if os.Getenv("DAS_DEBUG_JOB_YAML") == "true" {
		if yamlStr, err := RenderJobYAML(data); err == nil {
			tmpFile := filepath.Join(os.TempDir(), "loom-job-"+jobName+".yaml")
			os.WriteFile(tmpFile, []byte(yamlStr), 0644)
			log.Printf("[k8s] job yaml saved to %s", tmpFile)
		}
	}

	// init container 脚本：clone → chmod → 拉取 akasha gradle.properties
	initScript := fmt.Sprintf(
		`export GIT_SSH_COMMAND="ssh -i /keys/id_rsa -o StrictHostKeyChecking=no"
ref=%s
git clone --depth 1 --branch $ref %s /work/src
chmod -R u+w /work/src%s`,
		refStr(data), data.RepoURL, akashaFetchScript(e.cfg.AkashaAPIURL, data.AkashaBranch))

	// analyze container 脚本：gradlew → 回调（不再需要 -PdepBranch）
	analyzeScript := `set -e
./gradlew --init-script /scripts/das.gradle help -q 2>/tmp/gradle-stderr.log || {
  ESCAPED=$(cat /tmp/gradle-stderr.log | head -200 | python3 -c 'import sys,json; print(json.dumps(sys.stdin.read()))')
  curl -sf -X POST "$CALLBACK_URL" -H "Content-Type: application/json" -d "{\"error\": $ESCAPED}" || true
  exit 1
}
curl -sf -X POST "$CALLBACK_URL" -H "Content-Type: application/json" --data-binary @/work/src/das-output.json`

	callbackURL := fmt.Sprintf(
		"%s/das/callback?plan_id=%s&repo_id=%s&tag=%s",
		data.CallbackBaseURL, data.PlanID, data.RepoID, refStr(data))

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: data.Namespace,
			Labels: map[string]string{
				"app": "gps-das", "plan": data.PlanID, "repo": data.RepoID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            int32Ptr(1),
			ActiveDeadlineSeconds:   int64Ptr(int64(data.JobTimeout)),
			TTLSecondsAfterFinished: int32Ptr(600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "gps-das", "plan": data.PlanID},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes: []corev1.Volume{
						{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "init-script", VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: data.ConfigmapName},
							},
						}},
						{Name: "ssh-key", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName:  data.SSHSecretName,
								DefaultMode: int32Ptr(0400),
							},
						}},
						{Name: "gradle-cache", VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: data.GradleCachePVC,
								ReadOnly:  true,
							},
						}},
					},
					InitContainers: []corev1.Container{
						{
							Name:    "clone",
							Image:   data.GitImage,
							Command: []string{"sh", "-c"},
							Args:    []string{initScript},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "workspace", MountPath: "/work"},
								{Name: "ssh-key", MountPath: "/keys", ReadOnly: true},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:       "analyze",
							Image:      data.JDKImagePrefix + ":" + data.JDK,
							WorkingDir: "/work/src",
							Env: []corev1.EnvVar{
								{Name: "GRADLE_USER_HOME", Value: "/gradle-cache"},
								{Name: "CALLBACK_URL", Value: callbackURL},
							},
							Command: []string{"sh", "-c"},
							Args:    []string{analyzeScript},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "workspace", MountPath: "/work"},
								{Name: "init-script", MountPath: "/scripts", ReadOnly: true},
								{Name: "gradle-cache", MountPath: "/gradle-cache", ReadOnly: true},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	return e.clientset.BatchV1().Jobs(e.namespace).Create(ctx, job, metav1.CreateOptions{})
}

// waitForJob 通过 watch 等待 Job 完成。
func (e *K8sExecutor) waitForJob(ctx context.Context, jobName string) (*batchv1.Job, error) {
	watcher, err := e.clientset.BatchV1().Jobs(e.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + jobName,
	})
	if err != nil {
		return nil, fmt.Errorf("watch job: %w", err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			return nil, fmt.Errorf("watch error for job %s", jobName)
		}
		job, ok := event.Object.(*batchv1.Job)
		if !ok {
			continue
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed {
				return job, nil
			}
		}
	}

	return nil, fmt.Errorf("watch closed for job %s", jobName)
}

// deleteJobIfExists 删除同名 Job。
func (e *K8sExecutor) deleteJobIfExists(ctx context.Context, jobName string) {
	dp := metav1.DeletePropagationBackground
	err := e.clientset.BatchV1().Jobs(e.namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &dp,
	})
	if err != nil {
		log.Printf("[k8s] delete old job %s: %v (ignored if not found)", jobName, err)
	}
}

// --- helpers ---

// refStr 返回克隆用的 git ref（branch 优先于 tag）。
func refStr(data JobTemplateData) string {
	if data.Branch != "" {
		return data.Branch
	}
	return data.Tag
}

// akashaFetchScript 返回拉取 gradle.properties 的 shell 脚本片段。
// apiURL 为空时返回空字符串（跳过）。
func akashaFetchScript(apiURL, branch string) string {
	if apiURL == "" {
		return ""
	}
	return fmt.Sprintf(
		"\ncurl -sf -o /work/src/gradle.properties '%s?depBranch=%s' || true",
		apiURL, branch)
}

func int32Ptr(i int32) *int32 { return &i }
func int64Ptr(i int64) *int64 { return &i }
