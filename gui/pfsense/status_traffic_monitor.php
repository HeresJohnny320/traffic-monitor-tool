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
<iframe src="/traffic_monitor.php?p=" title="Traffic Monitor"
	style="display:block;width:100%;height:calc(100vh - 190px);min-height:640px;border:0"></iframe>
<?php include("foot.inc"); ?>
