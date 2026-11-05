// The chat page owns its height, so the shell makes main a viewport-tall
// column there. It covers the page itself and anything nested under it.
export function fillsMain(pathname: string) {
  return pathname === "/chat" || pathname.startsWith("/chat/");
}
