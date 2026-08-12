import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

/**
 * All six §6.2 top-level views, the `/` -> `/sessions` redirect, and the
 * catch-all NotFoundView. Only five of these six views are reachable from a
 * static sidebar link (SessionDetailView needs a session id) — see
 * AppShell.vue's nav list and its accompanying deviation note.
 */
export const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/sessions' },
  {
    path: '/sessions',
    name: 'sessions',
    component: () => import('@/views/SessionListView.vue'),
  },
  {
    path: '/sessions/:id',
    name: 'session-detail',
    component: () => import('@/views/SessionDetailView.vue'),
    props: true,
  },
  {
    path: '/tools',
    name: 'tools',
    component: () => import('@/views/ToolExplorerView.vue'),
  },
  {
    path: '/analytics',
    name: 'analytics',
    component: () => import('@/views/AnalyticsView.vue'),
  },
  {
    path: '/live',
    name: 'live',
    component: () => import('@/views/LiveView.vue'),
  },
  {
    path: '/data-quality',
    name: 'data-quality',
    component: () => import('@/views/DataQualityView.vue'),
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
