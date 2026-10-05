<?php
/*
 * Import pfSense's networks, interfaces and device names into Traffic Monitor
 * from the command line. Installed by install.sh as
 * /usr/local/share/traffic-monitor/pfsense_import.php and run:
 *   - by install.sh with --setup (also turns on softflowd and SNMP if needed),
 *   - every 15 minutes by cron (keeps device names and networks current).
 */

require_once("/usr/local/share/traffic-monitor/pfsense_import.inc");

$setup = in_array("--setup", $argv, true);
$res = tm_pfsense_import($setup);
if ($setup) {
	// keep device names and networks current
	install_cron_job("/usr/local/bin/php -f /usr/local/share/traffic-monitor/pfsense_import.php", true, "*/15");
}

if (!$res["ok"]) {
	fwrite(STDERR, "Traffic Monitor import failed: " . $res["error"] . "\n");
}
foreach (isset($res["notes"]) ? $res["notes"] : array() as $n) {
	echo "  - $n\n";
}
if ($res["ok"]) {
	$f = $res["found"];
	echo "  Found {$f["networks"]} networks, {$f["interfaces"]} interfaces, {$f["devices"]} named devices.\n";
	foreach ($res["changes"] as $c) {
		echo "  + $c\n";
	}
}
exit($res["ok"] ? 0 : 1);
