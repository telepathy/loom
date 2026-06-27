// API client for Loom DAS
const API = {
    async get(url) {
        const res = await fetch(url);
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${res.status}`);
        }
        return res.json();
    },

    async post(url, body) {
        const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${res.status}`);
        }
        return res.json();
    },

    getPlans()           { return this.get('/das/plans'); },
    getPlanDetail(id)    { return this.get('/das/plans/' + id); },
    getAnalyzeStatus(id) { return this.get('/das/analyze/' + id); },
    getRepos()           { return this.get('/das/repos'); },
    selfAnalyze(data)    { return this.post('/das/analyze/self', data); },
};

// Utilities
const Utils = {
    formatTime(ts) {
        if (!ts) return '-';
        const d = new Date(ts);
        return d.toLocaleString('zh-CN');
    },

    statusBadge(status) {
        const s = (status || '').toLowerCase();
        return `<span class="badge badge-${s}">${status}</span>`;
    },

    escapeHtml(str) {
        if (!str) return '';
        return str.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
    },

    plural(n, singular, plural) {
        return n === 1 ? singular : (plural || singular + 's');
    },
};
