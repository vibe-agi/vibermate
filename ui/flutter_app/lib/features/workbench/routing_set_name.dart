import '../../core/api/control_models.dart';
import '../../core/i18n/app_copy.dart';

/// The id of the routing set every install ships with, used when an agent
/// starts without --env. Its stored name and id never change; only what the
/// App shows for it does.
const builtInRoutingSetId = 'system_transparent';

/// What a routing set is called on screen: the localized built-in name for
/// the set every install ships with, and the stored name for any other.
/// Matching and search keep using the stored name as well.
String environmentDisplayName(EnvironmentRecord environment, AppCopy copy) =>
    environment.systemOwned && environment.id == builtInRoutingSetId
    ? copy('environment.builtin.name')
    : environment.name;

/// [environmentDisplayName] for a place that holds only a routing set's id
/// and the name the Runtime reported with it.
String routingSetDisplayName(String id, String? name, AppCopy copy) =>
    id == builtInRoutingSetId
    ? copy('environment.builtin.name')
    : (name == null || name.isEmpty ? id : name);

/// Whether [environment] is the set every install ships with.
bool isBuiltInRoutingSet(EnvironmentRecord environment) =>
    environment.systemOwned && environment.id == builtInRoutingSetId;
