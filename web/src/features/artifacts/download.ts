/** Starts a download of `url` as `name`. The server sends artifacts as attachments, so this never navigates. */
export function startDownload(url: string, name: string) {
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}
