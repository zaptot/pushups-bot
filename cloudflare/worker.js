// Прослойка между Discord и Discloud: Discloud ставит X-Frame-Options: DENY,
// из-за чего Discord не может показать казино во встроенном окне. Воркер
// пересылает запросы на сайт бота и убирает этот заголовок.
const ORIGIN = "pushups.discloud.dev";

export default {
  async fetch(request) {
    const url = new URL(request.url);
    url.protocol = "https:";
    url.hostname = ORIGIN;
    url.port = "";
    const upstream = await fetch(new Request(url, request));
    const response = new Response(upstream.body, upstream);
    response.headers.delete("X-Frame-Options");
    return response;
  },
};
