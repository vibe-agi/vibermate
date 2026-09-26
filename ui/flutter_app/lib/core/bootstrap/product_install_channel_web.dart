import 'product_install_channel_contract.dart';

Future<ProductInstallChannel> detectProductInstallChannel() async =>
    ProductInstallChannel.webServer;
