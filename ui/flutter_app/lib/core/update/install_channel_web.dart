import 'install_channel_types.dart';

Future<ProductInstallChannel> detectProductInstallChannel() async =>
    ProductInstallChannel.webServer;
