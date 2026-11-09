import { defineConfig } from 'vite';

export default defineConfig({
  server: {
    allowedHosts: true,
    proxy: {
      '/api': `http://localhost:${process.env.ACTIVITY_PORT ?? 8080}`,
    },
  },
});
