// PlanDetailPage — single plan view with repo results
const PlanDetailPage = {
    planId: '',

    async render(container, planId) {
        this.planId = planId;
        container.innerHTML = `
            <a href="#/" class="back-link">← 返回列表</a>
            <div id="detail-content"><div class="loading"><span class="spinner"></span> 加载中...</div></div>`;

        this._load();
    },

    async _load() {
        const area = document.getElementById('detail-content');
        try {
            const detail = await API.getPlanDetail(this.planId);
            this._renderDetail(area, detail);
        } catch (err) {
            area.innerHTML = `<div class="empty-state"><h3>加载失败</h3><p>${Utils.escapeHtml(err.message)}</p></div>`;
        }
    },

    _renderDetail(area, p) {
        let html = `
            <div class="card">
                <div class="page-header">
                    <h1><code>${Utils.escapeHtml(p.plan_id)}</code></h1>
                    ${Utils.statusBadge(p.status)}
                </div>
                <div class="meta-bar">
                    <span>Akasha 分支: <strong>${Utils.escapeHtml(p.akasha_branch) || '-'}</strong></span>
                    <span>仓库数: <strong>${p.repo_count}</strong></span>
                    <span>创建: <strong>${Utils.formatTime(p.created_at)}</strong></span>
                </div>
            </div>`;

        const repos = p.repos || [];
        if (repos.length === 0) {
            html += '<div class="card"><div class="empty-state"><h3>暂无仓库数据</h3></div></div>';
            area.innerHTML = html;
            return;
        }

        html += `<div class="card"><h2 style="margin-bottom:16px;font-size:18px;">仓库分析结果 (${repos.length})</h2>`;

        for (let i = 0; i < repos.length; i++) {
            const r = repos[i];
            const spCount = (r.subprojects || []).length;
            const edgeCount = (r.edges || []).length;

            html += `<div class="repo-row">
                <div class="repo-header" onclick="PlanDetailPage._toggleRepo(this)">
                    <span class="arrow">▶</span>
                    <span>${Utils.statusBadge(r.status)}</span>
                    <code>${Utils.escapeHtml(r.repo_id)}</code>
                    <span style="color:var(--text-dim);font-size:13px;">ref: ${Utils.escapeHtml(r.ref || '-')}</span>
                    <span style="color:var(--text-dim);font-size:13px;margin-left:auto;">
                        ${spCount} ${Utils.plural(spCount, '子项目', '子项目')} ·
                        ${edgeCount} ${Utils.plural(edgeCount, '依赖边', '依赖边')}
                    </span>
                </div>
                <div class="repo-detail">
                    ${r.error ? `<p style="color:var(--danger);margin-bottom:8px;">错误: ${Utils.escapeHtml(r.error)}</p>` : ''}
                    ${this._renderSubprojects(r.subprojects)}
                    ${this._renderEdges(r.edges)}
                </div>
            </div>`;
        }

        html += '</div>';
        area.innerHTML = html;
    },

    _renderSubprojects(sps) {
        if (!sps || sps.length === 0) return '';
        let html = '<h4>子项目</h4><table><thead><tr><th>Gradle Path</th><th>Group</th><th>Artifact</th><th>Version</th></tr></thead><tbody>';
        for (const sp of sps) {
            html += `<tr>
                <td><code>${Utils.escapeHtml(sp.gradle_path)}</code></td>
                <td>${Utils.escapeHtml(sp.group)}</td>
                <td>${Utils.escapeHtml(sp.artifact)}</td>
                <td>${Utils.escapeHtml(sp.version)}</td>
            </tr>`;
        }
        html += '</tbody></table>';
        return html;
    },

    _renderEdges(edges) {
        if (!edges || edges.length === 0) return '';
        let html = '<h4>依赖边</h4><table><thead><tr><th>From</th><th>To</th><th>Type</th></tr></thead><tbody>';
        for (const e of edges) {
            html += `<tr>
                <td><code>${Utils.escapeHtml(e.from)}</code></td>
                <td><code>${Utils.escapeHtml(e.to)}</code></td>
                <td><span class="badge ${e.type === 'project' ? 'badge-running' : 'badge-pending'}">${Utils.escapeHtml(e.type)}</span></td>
            </tr>`;
        }
        html += '</tbody></table>';
        return html;
    },

    _toggleRepo(header) {
        header.classList.toggle('open');
        const detail = header.nextElementSibling;
        detail.classList.toggle('open');
    },
};
