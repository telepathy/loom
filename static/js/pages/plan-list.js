// PlanListPage — list of all analysis plans + self-analyze launcher
const PlanListPage = {
    async render(container) {
        container.innerHTML = `
            <div class="page-header">
                <h1>分析记录</h1>
                <div class="toolbar">
                    <button class="btn btn-primary" id="btn-launch">发起分析</button>
                    <button class="btn btn-sm" id="btn-refresh">刷新</button>
                </div>
            </div>
            <div id="plan-table-area"><div class="loading"><span class="spinner"></span> 加载中...</div></div>
            <div id="modal-overlay" class="modal-overlay" style="display:none;"></div>`;

        document.getElementById('btn-refresh').onclick = () => this._load();
        document.getElementById('btn-launch').onclick = () => this._showModal();
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
                area.innerHTML += `<div class="empty-state"><h3>暂无分析记录</h3><p>点击"发起分析"开始新的依赖分析。</p></div>`;
                return;
            }

            let html = `<table><thead><tr>
                <th>Plan ID</th><th>Akasha</th><th>状态</th><th>仓库数</th><th>创建时间</th>
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

    async _showModal() {
        const overlay = document.getElementById('modal-overlay');
        overlay.innerHTML = `<div class="modal-box"><div class="spinner"></div> 加载仓库列表...</div>`;
        overlay.style.display = 'flex';
        overlay.onclick = (e) => { if (e.target === overlay) this._closeModal(); };

        try {
            const data = await API.getRepos();
            const repos = data.repos || [];
            this._renderModal(overlay, repos);
        } catch (err) {
            overlay.innerHTML = `<div class="modal-box"><p>加载失败: ${Utils.escapeHtml(err.message)}</p>
                <button class="btn btn-sm" onclick="PlanListPage._closeModal()">关闭</button></div>`;
        }
    },

    _renderModal(overlay, repos) {
        // Group repos by silo
        const silos = {};
        repos.forEach(r => {
            const sid = r.silo_id || 'default';
            if (!silos[sid]) silos[sid] = [];
            silos[sid].push(r);
        });

        let html = `<div class="modal-box" style="max-width:700px;max-height:80vh;overflow-y:auto;">
            <h2 style="margin-bottom:4px;">发起依赖分析</h2>
            <p style="color:var(--text-dim);font-size:13px;margin-bottom:12px;">选择要分析的仓库</p>
            <div style="margin-bottom:8px;">
                <label style="cursor:pointer;font-size:13px;">
                    <input type="checkbox" id="select-all" onchange="PlanListPage._toggleAll(this)"> 全选
                </label>
            </div>
            <div id="repo-list" style="margin-bottom:16px;">`;

        const sortedSilos = Object.keys(silos).sort();
        sortedSilos.forEach(sid => {
            html += `<div style="margin-bottom:8px;">
                <div style="font-weight:600;font-size:13px;color:var(--primary);margin-bottom:4px;">${Utils.escapeHtml(sid)}</div>`;
            silos[sid].forEach(r => {
                html += `<label style="display:inline-flex;align-items:center;gap:4px;margin:2px 12px 2px 0;
                    cursor:pointer;font-size:13px;">
                    <input type="checkbox" class="repo-cb" value="${Utils.escapeHtml(r.id)}"
                           data-silo="${Utils.escapeHtml(sid)}">
                    ${Utils.escapeHtml(r.name)}
                    <span style="color:var(--text-dim);">(${Utils.escapeHtml(r.release_branch || 'main')})</span>
                </label>`;
            });
            html += `</div>`;
        });

        html += `</div>
            <div style="margin-bottom:12px;">
                <label style="font-size:13px;">Akasha 分支
                    <input type="text" id="akasha-branch" placeholder="如 202603"
                           style="margin-left:8px;padding:6px 10px;border-radius:6px;
                                  border:1px solid var(--border);background:var(--bg-input);
                                  color:var(--text);width:160px;"/>
                </label>
            </div>
            <div style="display:flex;gap:8px;">
                <button class="btn btn-primary" id="btn-submit">开始分析</button>
                <button class="btn" onclick="PlanListPage._closeModal()">取消</button>
            </div>
        </div>`;

        overlay.innerHTML = html;
        overlay.style.display = 'flex';
        overlay.onclick = (e) => { if (e.target === overlay) this._closeModal(); };
        document.getElementById('btn-submit').onclick = () => this._submit();
    },

    _toggleAll(el) {
        document.querySelectorAll('.repo-cb').forEach(cb => { cb.checked = el.checked; });
    },

    async _submit() {
        const cbs = document.querySelectorAll('.repo-cb:checked');
        const repoIds = Array.from(cbs).map(cb => cb.value);
        if (repoIds.length === 0) {
            alert('请至少选择一个仓库');
            return;
        }
        const branch = document.getElementById('akasha-branch').value.trim();
        if (!branch) {
            alert('请输入 Akasha 分支');
            return;
        }

        const btn = document.getElementById('btn-submit');
        btn.disabled = true;
        btn.textContent = '提交中...';
        try {
            const result = await API.selfAnalyze({ repo_ids: repoIds, akasha_branch: branch });
            this._closeModal();
            alert(`分析已启动: ${result.plan_id}\n共 ${result.repo_count} 个仓库`);
            this._load(); // refresh plan list
        } catch (err) {
            alert('发起失败: ' + err.message);
            btn.disabled = false;
            btn.textContent = '开始分析';
        }
    },

    _closeModal() {
        const overlay = document.getElementById('modal-overlay');
        overlay.style.display = 'none';
        overlay.innerHTML = '';
    },
};
