<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { useNodes } from '@/composables/useNodes'

const route = useRoute()
const isAdminRoute = computed(() => route.path.startsWith('/admin'))
const { meta, onlineNodes } = useNodes()
</script>

<template>
  <div class="app-shell">
    <header class="top-nav">
      <div class="top-nav-inner">
        <div class="nav-left">
          <RouterLink to="/" class="brand">
            <span class="brand-logo">11DOG</span>
            <span>{{ meta?.network_name || 'DN11' }} 测速</span>
            <span class="brand-dn11">DN11</span>
          </RouterLink>

          <nav v-if="!isAdminRoute" class="nav-tabs">
            <RouterLink to="/" class="nav-tab">Ping 测试</RouterLink>
            <RouterLink to="/tcping" class="nav-tab">TCPing</RouterLink>
            <RouterLink to="/http" class="nav-tab">HTTP 测速</RouterLink>
            <RouterLink to="/traceroute" class="nav-tab">路由追踪</RouterLink>
            <RouterLink to="/dns" class="nav-tab">DNS 查询</RouterLink>
          </nav>

          <nav v-else class="nav-tabs">
            <span class="nav-tab router-link-active">运维管理后台</span>
            <RouterLink to="/" class="nav-tab">返回测速前台</RouterLink>
          </nav>
        </div>

        <div v-if="!isAdminRoute" class="nav-right">
          <span class="nav-pill">
            <span class="dot-online"></span>
            <span class="mono">在线监测点 {{ onlineNodes.length }}</span>
          </span>
        </div>
      </div>
    </header>

    <main class="container">
      <RouterView />
    </main>
  </div>
</template>
