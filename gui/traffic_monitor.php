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

define("TM_DIR", "/usr/local/etc/traffic-monitor");
define("TM_BASE", "/traffic_monitor.php?p=");

function tm_fail($code, $msg)
{
	http_response_code(tm_status($code));
	header("Content-Type: text/plain; charset=utf-8");
	echo $msg, "\n";
	exit;
}

// tm_target is http://host:port of the service, from web.listen in its settings.
function tm_target()
{
	$host = "127.0.0.1";
	$port = "8080";
	$inweb = false;
	$lines = @file(TM_DIR . "/traffic-monitor.yaml");
	foreach ($lines ? $lines : array() as $line) {
		if (preg_match('/^[^\s#]/', $line)) {
			$inweb = (strncmp($line, "web:", 4) === 0);
		} elseif ($inweb && preg_match('/^\s+listen:\s*["\']?([^"\'\s#]*)/', $line, $m)) {
			$i = strrpos($m[1], ":");
			if ($i !== false) {
				$h = trim(substr($m[1], 0, $i), "[]");
				$port = substr($m[1], $i + 1);
				if ($h !== "" && $h !== "0.0.0.0" && $h !== "::") {
					$host = $h;
				}
			}
			break;
		}
	}
	if (strpos($host, ":") !== false) {
		$host = "[$host]";
	}
	return "http://$host:" . (int)$port;
}

$token = trim((string)@file_get_contents(TM_DIR . "/gui-token"));
if ($token === "") {
	tm_fail(503, "Traffic Monitor: " . TM_DIR . "/gui-token is missing or unreadable; re-run install.sh.");
}
$target = tm_target();
if (!function_exists("curl_init")) {
	tm_fail(500, "Traffic Monitor: PHP's curl extension is not available.");
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

$headers = array(
	"X-Traffic-Monitor-Gui: $token",
	"X-Traffic-Monitor-Base: " . TM_BASE,
	"X-Traffic-Monitor-Gui-Csrf: $tm_csrf",
	"Expect:",
);
if ($changing) {
	$headers[] = "X-Traffic-Monitor: 1";
	$headers[] = "Content-Type: application/json";
}

$resp_headers = array();
$ch = curl_init($target . "/" . $path);
curl_setopt_array($ch, array(
	CURLOPT_CUSTOMREQUEST => $method,
	CURLOPT_NOBODY => ($method === "HEAD"),
	CURLOPT_HTTPHEADER => $headers,
	CURLOPT_RETURNTRANSFER => true,
	CURLOPT_FOLLOWLOCATION => false,
	CURLOPT_CONNECTTIMEOUT => 5,
	CURLOPT_TIMEOUT => 60,
	CURLOPT_HEADERFUNCTION => function ($ch, $line) use (&$resp_headers) {
		$parts = explode(":", $line, 2);
		if (count($parts) === 2) {
			$resp_headers[strtolower(trim($parts[0]))] = trim($parts[1]);
		}
		return strlen($line);
	},
));
if ($changing) {
	curl_setopt($ch, CURLOPT_POSTFIELDS, file_get_contents("php://input"));
}
$body = curl_exec($ch);
if ($body === false) {
	tm_fail(502, "Traffic Monitor is not responding at $target (" . curl_error($ch) . ").\n" .
		"Is the service running? Check with: service traffic_monitor.sh status (pfSense) or service traffic_monitor status (OPNsense).\n" .
		"Its log is under Status → System Logs → General (traffic-monitor).");
}
http_response_code(tm_status(curl_getinfo($ch, CURLINFO_RESPONSE_CODE)));
curl_close($ch);

foreach (array("content-type", "cache-control", "content-security-policy", "x-frame-options", "x-content-type-options", "referrer-policy") as $h) {
	if (isset($resp_headers[$h])) {
		header("$h: " . $resp_headers[$h]);
	}
}
echo $body;
