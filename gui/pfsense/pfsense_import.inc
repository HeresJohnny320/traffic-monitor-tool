<?php
/*
 * Reads what Traffic Monitor needs from this pfSense and hands it over:
 *   - networks: every enabled non-WAN interface's subnets (LAN, VLANs, OPT,
 *     assigned VPN interfaces) and OpenVPN server tunnel networks;
 *   - interfaces: real name → description and role (wan/lan/vlan/vpn);
 *   - device names: DHCP leases, DHCP static mappings, DNS Resolver/Forwarder
 *     host overrides;
 *   - with $setup: turns on what produces the data, unless you already set it
 *     up differently: softflowd (per-device traffic, sent to 127.0.0.1) and the
 *     SNMP agent on localhost (WAN/VLAN/VPN interface totals).
 * Traffic Monitor only adds what is missing; names and networks you set keep
 * priority (see ApplyImport in internal/config/import.go).
 *
 * Installed by install.sh as /usr/local/share/traffic-monitor/pfsense_import.inc;
 * used by pfsense_import.php (install, 15-minute cron) and Settings → Import.
 */

require_once("/usr/local/share/traffic-monitor/traffic_monitor.inc");
foreach (array("config.inc", "util.inc", "interfaces.inc", "services.inc", "system.inc", "pkg-utils.inc") as $inc) {
	require_once($inc);
}
@include_once("services_dhcp.inc"); // pfSense 2.8: DHCP backend helpers

// tm_kind is Traffic Monitor's network kind for a non-WAN interface.
function tm_kind($real)
{
	if (interface_is_vlan($real) !== NULL || strpos($real, ".") !== false) {
		return "vlan";
	}
	if (preg_match('/^(ovpn|tun_wg|wg|ipsec|tun|tap|l2tp|pppoe)/', $real)) {
		return "vpn";
	}
	return "lan";
}

function tm_pfsense_import($setup)
{
	$notes = array();
	if (!function_exists("config_get_path")) {
		return array("ok" => false, "error" => "Importing needs pfSense 2.7 or later.");
	}

	// what Traffic Monitor listens on, for softflowd
	list($code, , $body) = tm_call("GET", "api/settings");
	if ($code !== 200) {
		return array("ok" => false, "error" => $code === 0 ? $body : "Traffic Monitor answered $code: $body");
	}
	$settings = json_decode($body, true);
	$listen = isset($settings["config"]["netflow"]["listen"]) ? $settings["config"]["netflow"]["listen"] : ":2055";
	$nf_port = (int)substr($listen, strrpos($listen, ":") + 1);

	// interfaces and their networks
	$networks = array();
	$interfaces = array();
	$flow_ifs = array();
	$seen_cidrs = array();
	foreach (config_get_path("interfaces", array()) as $friendly => $ifcfg) {
		if (!isset($ifcfg["enable"])) {
			continue;
		}
		$descr = !empty($ifcfg["descr"]) ? $ifcfg["descr"] : strtoupper($friendly);
		$real = get_real_interface($friendly);
		$wan = interface_has_gateway($friendly) || interface_has_gatewayv6($friendly);
		$kind = $wan ? "wan" : tm_kind($real);
		if (!empty($real)) {
			$interfaces[$real] = array("label" => $descr, "kind" => $kind);
		}
		if ($wan) {
			continue; // after NAT every device looks like the WAN address
		}
		$cidrs = array();
		$ip = get_interface_ip($friendly);
		$bits = get_interface_subnet($friendly);
		if (is_ipaddrv4($ip) && $bits) {
			$cidrs[] = gen_subnet($ip, $bits) . "/" . $bits;
		}
		$ip6 = get_interface_ipv6($friendly);
		$bits6 = get_interface_subnetv6($friendly);
		if (is_ipaddrv6($ip6) && $bits6 && !is_linklocal($ip6)) {
			$cidrs[] = gen_subnetv6($ip6, $bits6) . "/" . $bits6;
		}
		if (empty($cidrs)) {
			continue;
		}
		$networks[] = array("name" => $descr, "kind" => $kind, "cidr" => $cidrs);
		$seen_cidrs = array_merge($seen_cidrs, $cidrs);
		$flow_ifs[] = $friendly;
	}
	// OpenVPN servers whose tunnel isn't an assigned interface
	foreach (config_get_path("openvpn/openvpn-server", array()) as $srv) {
		if (isset($srv["disable"])) {
			continue;
		}
		$cidrs = array();
		foreach (array("tunnel_network", "tunnel_networkv6") as $k) {
			if (!empty($srv[$k]) && is_subnet($srv[$k]) && !in_array($srv[$k], $seen_cidrs)) {
				$cidrs[] = $srv[$k];
			}
		}
		if (!empty($cidrs)) {
			$name = "OpenVPN " . (!empty($srv["description"]) ? $srv["description"] : $srv["vpnid"]);
			$networks[] = array("name" => $name, "kind" => "vpn", "cidr" => $cidrs);
		}
	}

	// device names: leases first, then static mappings and DNS overrides win
	$hosts = array();
	if (function_exists("system_get_dhcpleases")) {
		$leases = system_get_dhcpleases();
		foreach (isset($leases["lease"]) ? $leases["lease"] : array() as $l) {
			$name = !empty($l["hostname"]) ? $l["hostname"] : (!empty($l["descr"]) ? $l["descr"] : "");
			if (!empty($l["ip"]) && $name !== "") {
				$hosts[$l["ip"]] = $name;
			}
		}
	}
	foreach (array("dhcpd" => "ipaddr", "dhcpdv6" => "ipaddrv6") as $section => $ipkey) {
		foreach (config_get_path($section, array()) as $dhcp) {
			foreach ((is_array($dhcp) && isset($dhcp["staticmap"]) && is_array($dhcp["staticmap"])) ? $dhcp["staticmap"] : array() as $m) {
				$name = !empty($m["hostname"]) ? $m["hostname"] : (!empty($m["descr"]) ? $m["descr"] : "");
				if (!empty($m[$ipkey]) && $name !== "") {
					$hosts[$m[$ipkey]] = $name;
				}
			}
		}
	}
	foreach (array("unbound/hosts", "dnsmasq/hosts") as $section) {
		foreach (config_get_path($section, array()) as $h) {
			if (empty($h["host"]) || empty($h["ip"])) {
				continue;
			}
			foreach (explode(",", $h["ip"]) as $ip) {
				$hosts[trim($ip)] = $h["host"];
			}
		}
	}

	$snmp = null;
	if ($setup) {
		$notes = array_merge($notes, tm_setup_softflowd($flow_ifs, $nf_port));
	}
	list($snmp, $snmp_notes) = tm_snmp($setup);
	$notes = array_merge($notes, $snmp_notes);

	$payload = array(
		"source" => "pfSense " . config_get_path("system/hostname", "") . "." . config_get_path("system/domain", ""),
		"networks" => $networks,
		"interfaces" => (object)$interfaces,
		"hosts" => (object)$hosts,
		"snmp" => $snmp,
	);
	list($code, , $body) = tm_call("POST", "api/import", json_encode($payload));
	if ($code !== 200) {
		return array("ok" => false, "error" => $code === 0 ? $body : "Traffic Monitor answered $code: $body", "notes" => $notes);
	}
	$res = json_decode($body, true);
	return array(
		"ok" => true,
		"changes" => isset($res["changes"]) ? $res["changes"] : array(),
		"notes" => $notes,
		"found" => array("networks" => count($networks), "interfaces" => count($interfaces), "devices" => count($hosts)),
	);
}

/*
 * tm_setup_softflowd points softflowd at Traffic Monitor on every inside
 * interface. It leaves softflowd alone if it already sends somewhere else.
 */
function tm_setup_softflowd($flow_ifs, $nf_port)
{
	if (!is_package_installed("softflowd")) {
		return array("softflowd is not installed: System → Package Manager → Available Packages → softflowd (install.sh does this).");
	}
	if (empty($flow_ifs)) {
		return array("No inside interfaces with an address were found for softflowd.");
	}
	$cur = config_get_path("installedpackages/softflowd/config/0", array());
	if (!is_array($cur)) {
		$cur = array();
	}
	$enabled = !empty($cur) && (!isset($cur["enable"]) || $cur["enable"] != "off");
	$host = isset($cur["host"]) ? $cur["host"] : "";
	$port = isset($cur["port"]) ? (int)$cur["port"] : 0;
	$ours = in_array($host, array("127.0.0.1", "::1", "localhost")) && $port === $nf_port;
	if ($enabled && !$ours) {
		return array("softflowd already sends flows to $host:$port, so it was left as is. To use Traffic Monitor, set its Host to 127.0.0.1 and Port to $nf_port under Services → softflowd.");
	}
	$want = $cur;
	$want["enable"] = "enabled";
	$want["interface"] = implode(",", $flow_ifs);
	$want["host"] = "127.0.0.1";
	$want["port"] = (string)$nf_port;
	if (empty($want["version"]) || !in_array($want["version"], array("5", "9", "10"))) {
		$want["version"] = "9";
	}
	if (empty($want["flowtracking"])) {
		$want["flowtracking"] = "full";
	}
	if (empty($want["timeout-maxlife"])) {
		$want["timeout-maxlife"] = "60"; // report long transfers every minute for live rates
	}
	if ($want == $cur) {
		return array();
	}
	config_set_path("installedpackages/softflowd/config/0", $want);
	write_config("Traffic Monitor: softflowd sends flows from " . $want["interface"] . " to 127.0.0.1:$nf_port");
	require_once("/usr/local/pkg/softflowd.inc");
	sync_package_softflowd();
	return array("softflowd now sends flows from " . $want["interface"] . " to Traffic Monitor.");
}

/*
 * tm_snmp returns how Traffic Monitor can poll this firewall's SNMP agent
 * (null if it can't), turning the agent on for localhost only when $setup
 * and it is off.
 */
function tm_snmp($setup)
{
	if (!config_path_enabled("snmpd")) {
		if (!$setup) {
			return array(null, array());
		}
		$community = bin2hex(random_bytes(12));
		config_set_path("snmpd/enable", true);
		config_set_path("snmpd/pollport", "161");
		config_set_path("snmpd/rocommunity", $community);
		config_set_path("snmpd/bindip", "127.0.0.1");
		config_set_path("snmpd/ipprotocol", "4");
		if (!is_array(config_get_path("snmpd/modules"))) {
			config_set_path("snmpd/modules", array());
		}
		config_set_path("snmpd/modules/mibii", true);
		write_config("Traffic Monitor: SNMP agent on localhost for interface totals");
		services_snmpd_configure();
		return array(array("target" => "127.0.0.1", "port" => 161, "community" => $community),
			array("SNMP is on for this firewall only (localhost) so Traffic Monitor can show WAN/VLAN/VPN totals."));
	}
	if (!config_path_enabled("snmpd/modules", "mibii")) {
		return array(null, array("SNMP is on but its MibII module is off; turn it on under Services → SNMP for interface totals."));
	}
	$target = "127.0.0.1";
	$bind = array_filter(explode(",", config_get_path("snmpd/bindip", "")));
	if (!empty($bind) && !in_array("127.0.0.1", $bind) && !in_array("lo0", $bind)) {
		$target = "";
		foreach ($bind as $b) {
			$ip = is_ipaddr($b) ? $b : get_interface_ip($b);
			if (is_ipaddrv4($ip)) {
				$target = $ip;
				break;
			}
		}
	}
	if ($target === "") {
		return array(null, array("SNMP is bound to addresses Traffic Monitor can't reach; bind it to Localhost under Services → SNMP."));
	}
	return array(array("target" => $target, "port" => (int)config_get_path("snmpd/pollport", "161"),
		"community" => config_get_path("snmpd/rocommunity", "public")), array());
}
