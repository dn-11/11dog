import { createRouter, createWebHistory } from 'vue-router'
import AdminView from '@/views/AdminView.vue'
import DnsView from '@/views/DnsView.vue'
import HttpView from '@/views/HttpView.vue'
import PingView from '@/views/PingView.vue'
import TcpingView from '@/views/TcpingView.vue'
import TracerouteView from '@/views/TracerouteView.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'ping', component: PingView },
    { path: '/tcping', name: 'tcping', component: TcpingView },
    { path: '/http', name: 'http', component: HttpView },
    { path: '/traceroute', name: 'traceroute', component: TracerouteView },
    { path: '/dns', name: 'dns', component: DnsView },
    { path: '/nodes', redirect: '/' },
    { path: '/admin', name: 'admin', component: AdminView }
  ]
})

export default router
