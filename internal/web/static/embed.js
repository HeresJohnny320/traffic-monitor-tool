// Inside the pfSense/OPNsense web UI, every request goes through the
// firewall's proxy page; the server fills in these tags then (see gui.go).
const meta = name => document.querySelector(`meta[name="${name}"]`)?.content || "";
const base = meta("tm-base");
const csrf = meta("tm-csrf");

export const embedded = base !== "";
export const apiURL = path => `${base}api/${path}`;
// headers for requests that change something
export const changeHeaders = { "Content-Type": "application/json", "X-Traffic-Monitor": "1", ...(csrf && { "X-CSRFToken": csrf }) };
