// PlanListPage — list of all analysis plans
const PlanListPage = {
    async render(container) {
        container.innerHTML = `
            <div class="page-header">
                <h1>分析记录</h1>
                <div class="toolbar">
                    <button class="btn btn-sm" id="btn-refresh">⟳ 刷新</button>
                </div>
            </div>
            <div id="plan-table-area"><div class="loading"><span class="spinner"></span> 加载中...</div></div>`;

        document.getElementById('btn-refresh').onclick = () => this._load();
        this._load();
    },

    async _load() {
        const area = document.getElementById('plan-table-area');
        try {
            const data = await API.getPlans();
            const plans = data.plans || [];

            if (data.notice) {
                area.innerHTML = `<div class="notice">${Utils.escapeHtml(data.notice)}</div>`;
            }

            if (plans.length === 0) {
                area.innerHTML += `
                    <div class="empty-state">
                        <h3>暂无分析记录</h3>
                        <p>发起一次依赖分析后，结果将显示在这里。</p>
                    </div>`;
                return;
            }

            let html = `<table><thead><tr>
                <th>Plan ID</th><th>Akasha 分支</th><th>状态</th><th>仓库数</th><th>创建时间</th>
            </tr></thead><tbody>`;

            for (const p of plans) {
                html += `<tr class="clickable" onclick="window.location.hash='#/plan/${Utils.escapeHtml(p.plan_id)}'">
                    <td><code>${Utils.escapeHtml(p.plan_id)}</code></td>
                    <td>${Utils.escapeHtml(p.akasha_branch) || '-'}</td>
                    <td>${Utils.statusBadge(p.status)}</td>
                    <td>${p.repo_count}</td>
                    <td>${Utils.formatTime(p.created_at)}</td>
                </tr>`;
            }
            html += '</tbody></table>';
            area.innerHTML = html;
        } catch (err) {
            area.innerHTML = `<div class="empty-state"><h3>加载失败</h3><p>${Utils.escapeHtml(err.message)}</p></div>`;
        }
    },
};
