// App — simple hash-based SPA router
const App = {
    currentPage: null,
    container: null,

    init() {
        this.container = document.getElementById('app');
        window.addEventListener('hashchange', () => this.route());
        this.route();
    },

    route() {
        const hash = window.location.hash || '#/';

        // Cleanup previous page
        if (this.currentPage && this.currentPage.destroy) {
            this.currentPage.destroy();
        }
        this.currentPage = null;

        const planMatch = hash.match(/^#\/plan\/(.+)$/);
        if (planMatch) {
            this.currentPage = PlanDetailPage;
            PlanDetailPage.render(this.container, decodeURIComponent(planMatch[1]));
            return;
        }

        // Default: plan list
        this.currentPage = PlanListPage;
        PlanListPage.render(this.container);
    },
};

document.addEventListener('DOMContentLoaded', () => App.init());
