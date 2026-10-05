<?php
/*
 * Shared by the Traffic Monitor pages on pfSense / OPNsense: how to reach the
 * Traffic Monitor service on this firewall. Installed by install.sh as
 * /usr/local/share/traffic-monitor/traffic_monitor.inc.
 */

define("TM_DIR", "/usr/local/etc/traffic-monitor");
define("TM_BASE", "/traffic_monitor.php?p=");

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

// tm_token is the secret that tells the service a request comes from here.
function tm_token()
{
	return trim((string)@file_get_contents(TM_DIR . "/gui-token"));
}

/*
 * tm_call sends a request to the service as the firewall and returns
 * array(status, lowercased response headers, body); status 0 = no answer
 * (the body then says why).
 */
function tm_call($method, $path, $body = null, $extra_headers = array())
{
	$token = tm_token();
	if ($token === "") {
		return array(0, array(), TM_DIR . "/gui-token is missing or unreadable; re-run install.sh.");
	}
	if (!function_exists("curl_init")) {
		return array(0, array(), "PHP's curl extension is not available.");
	}
	$headers = array_merge(array(
		"X-Traffic-Monitor-Gui: $token",
		"X-Traffic-Monitor-Base: " . TM_BASE,
		"Expect:",
	), $extra_headers);
	if ($body !== null) {
		$headers[] = "X-Traffic-Monitor: 1";
		$headers[] = "Content-Type: application/json";
	}
	$resp_headers = array();
	$target = tm_target();
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
	if ($body !== null) {
		curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
	}
	$out = curl_exec($ch);
	if ($out === false) {
		$err = curl_error($ch);
		curl_close($ch);
		return array(0, array(), "Traffic Monitor is not responding at $target ($err).\n" .
			"Is the service running? Check with: service traffic_monitor.sh status (pfSense) or service traffic_monitor status (OPNsense).\n" .
			"Its log is under Status → System Logs → General (traffic-monitor).");
	}
	$status = curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
	curl_close($ch);
	return array($status, $resp_headers, $out);
}
