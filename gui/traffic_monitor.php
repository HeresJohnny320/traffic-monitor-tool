<?php
/*
 * Traffic Monitor inside the pfSense / OPNsense web UI.
 *
 * Installed by Traffic Monitor's install.sh as /usr/local/www/traffic_monitor.php.
 * Only users logged in to the firewall get here (guiconfig.inc). Requests are
 * passed to the Traffic Monitor service on this firewall as
 * /traffic_monitor.php?p=<path>, with the secret from gui-token so the service
 * skips its own login and lets the firewall's pages frame it.
 */

// pfSense's web server replaces 404 and 5xx answers with its own error page,
// which would hide what went wrong; those are sent as 424 instead, with the
// original code in X-Traffic-Monitor-Status. The dashboard only checks r.ok.
define("TM_ERROR_STATUS", 424);
function tm_status($code)
{
	if ($code == 404 || $code >= 500) {
		header("X-Traffic-Monitor-Status: $code");
		return TM_ERROR_STATUS;
	}
	return $code;
}

// show a crash in this page instead of the web server's blank error page
register_shutdown_function(function () {
	$e = error_get_last();
	if ($e && ($e["type"] & (E_ERROR | E_PARSE | E_CORE_ERROR | E_COMPILE_ERROR)) && !headers_sent()) {
		http_response_code(TM_ERROR_STATUS);
		header("Content-Type: text/plain; charset=utf-8");
		echo "Traffic Monitor: PHP error: {$e["message"]} in {$e["file"]}:{$e["line"]}\n";
	}
});

$nocsrf = true; // pfSense: the dashboard's own CSRF header is checked below instead
require_once("guiconfig.inc");

// OPNsense checks its CSRF token on every change; hand it to the dashboard
$tm_csrf = isset($_SESSION['$PHALCON/CSRF$']) ? (string)$_SESSION['$PHALCON/CSRF$'] : "";
session_write_close(); // don't hold the session lock while the dashboard polls

require_once("/usr/local/share/traffic-monitor/traffic_monitor.inc");

function tm_fail($code, $msg)
{
	http_response_code(tm_status($code));
	header("Content-Type: text/plain; charset=utf-8");
	echo $msg, "\n";
	exit;
}

// p=<path>[?query], passed through as sent (not decoded)
$qs = isset($_SERVER["QUERY_STRING"]) ? $_SERVER["QUERY_STRING"] : "";
$path = (strncmp($qs, "p=", 2) === 0) ? substr($qs, 2) : "";
if (!preg_match('#^[A-Za-z0-9_./-]*(\?.*)?$#', $path) || strpos($path, "..") !== false) {
	tm_fail(400, "Traffic Monitor: bad path");
}

$method = $_SERVER["REQUEST_METHOD"];
if (!in_array($method, array("GET", "HEAD", "POST", "PUT"), true)) {
	tm_fail(405, "Traffic Monitor: method not allowed");
}
// Changes must carry the dashboard's custom header, which other websites
// can't add to a request (pfSense's own CSRF check is off for this page).
$changing = ($method === "POST" || $method === "PUT");
if ($changing && (!isset($_SERVER["HTTP_X_TRAFFIC_MONITOR"]) || $_SERVER["HTTP_X_TRAFFIC_MONITOR"] !== "1")) {
	tm_fail(403, "Traffic Monitor: missing X-Traffic-Monitor header");
}

// Settings → Import from pfSense: read the firewall's setup and send it to the service
if ($path === "_import") {
	$import = "/usr/local/share/traffic-monitor/pfsense_import.inc";
	if (!file_exists($import)) {
		tm_fail(404, "Traffic Monitor: importing is only available on pfSense.");
	}
	if (!$changing) {
		tm_fail(405, "Traffic Monitor: start an import with POST (Settings → Import from pfSense).");
	}
	require_once($import);
	$res = tm_pfsense_import(true);
	http_response_code($res["ok"] ? 200 : TM_ERROR_STATUS);
	header("Content-Type: application/json");
	echo json_encode($res);
	exit;
}

list($status, $resp_headers, $body) = tm_call($method, $path,
	$changing ? file_get_contents("php://input") : null,
	array("X-Traffic-Monitor-Gui-Csrf: $tm_csrf"));
if ($status === 0) {
	tm_fail(502, "Traffic Monitor: " . $body);
}
http_response_code(tm_status($status));

foreach (array("content-type", "cache-control", "content-security-policy", "x-frame-options", "x-content-type-options", "referrer-policy") as $h) {
	if (isset($resp_headers[$h])) {
		header("$h: " . $resp_headers[$h]);
	}
}
echo $body;
