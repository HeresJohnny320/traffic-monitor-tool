<?php
/*
 * Status → Traffic Monitor. Installed by Traffic Monitor's install.sh as
 * /usr/local/www/status_traffic_monitor.php; the dashboard itself comes
 * through traffic_monitor.php.
 */
require_once("guiconfig.inc");

$pgtitle = array(gettext("Status"), "Traffic Monitor");
include("head.inc");
?>
<iframe id="traffic-monitor" src="/traffic_monitor.php?p=" title="Traffic Monitor"
	style="display:block;width:100%;height:calc(100vh - 190px);min-height:420px;border:0"></iframe>
<script>
// Use the whole screen: this page drops pfSense's fixed content width, and the
// dashboard fills the window down to its bottom edge (one scrollbar, inside).
(function () {
	var frame = document.getElementById("traffic-monitor");
	var box = frame.closest(".container");
	if (box) {
		box.style.width = "auto";
		box.style.maxWidth = "none";
	}
	function fit() {
		var top = frame.getBoundingClientRect().top + window.pageYOffset;
		frame.style.height = Math.max(420, window.innerHeight - top - 12) + "px";
	}
	window.addEventListener("resize", fit);
	fit();
})();
</script>
<?php include("foot.inc"); ?>
