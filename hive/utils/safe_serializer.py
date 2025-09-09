import json
from decimal import Decimal
from aiocache.serializers import BaseSerializer

# Try to import RowProxy for type checking
try:
    from aiopg.sa.result import RowProxy
except ImportError:
    # Create a dummy class for type checking if aiopg isn't available
    class RowProxy:
        pass

class SafeUniversalSerializer(BaseSerializer):
    """
    Serializer that:
    1. Preserves RowProxy column information (e.g., parent_id, child_ids)
    2. Adds explicit "RowProxy" type handling to fix deserialization errors
    3. Maintains data types (tuples, lists, Decimals) correctly
    """
    
    def _convert_to_json_compatible(self, value):
        """Convert Python objects to JSON-serializable format (preserves columns for RowProxy)"""
        try:
            # Basic types (int/float/str/bool/None)
            if isinstance(value, (int, float, str, bool, type(None))):
                return {"type": type(value).__name__, "value": value}
                
            # Decimal (preserve precision with string)
            elif isinstance(value, Decimal):
                return {"type": "Decimal", "value": str(value)}
                
            # Tuples (e.g., nested tuples in data)
            elif isinstance(value, tuple):
                return {
                    "type": "tuple",
                    "value": [self._convert_to_json_compatible(item) for item in value]
                }
                
            # Lists (e.g., child_ids list)
            elif isinstance(value, list):
                return {
                    "type": "list",
                    "value": [self._convert_to_json_compatible(item) for item in value]
                }
                
            # Dictionaries (for general dict data)
            elif isinstance(value, dict):
                return {
                    "type": "dict",
                    "value": {k: self._convert_to_json_compatible(v) for k, v in value.items()}
                }
                
            # RowProxy: Preserve columns (convert to dict with column names)
            elif isinstance(value, RowProxy):
                # Convert RowProxy to a dict (retains column keys like "parent_id")
                row_dict = dict(value)
                # Mark as "RowProxy" type for deserialization (critical fix!)
                return {
                    "type": "RowProxy",
                    "value": self._convert_to_json_compatible(row_dict)
                }
                
            # Reject unsupported types
            else:
                raise TypeError(
                    f"Unsupported type: {type(value).__name__} "
                    f"(value: {str(value)[:100]})"
                )
                
        except Exception as e:
            raise ValueError(
                f"Conversion failed for {type(value)}: {str(e)}"
            ) from e
    
    def _restore_from_json_compatible(self, data, path="root"):
        """Restore Python objects (adds explicit RowProxy handling to fix the error)"""
        try:
            # Skip processing if data isn't in our expected format
            if not isinstance(data, dict) or "type" not in data or "value" not in data:
                return data
                
            data_type = data["type"]
            value = data["value"]
            
            # Critical Fix: Handle "RowProxy" type marker (resolves the error)
            # Returns the dict with column names (e.g., {"parent_id": 123, "child_ids": [...]})
            if data_type == "RowProxy":
                return self._restore_from_json_compatible(value, f"{path}[RowProxy]")
                
            # Restore tuples
            elif data_type == "tuple":
                return tuple(
                    self._restore_from_json_compatible(item, f"{path}[tuple-item]") 
                    for item in value
                )
                
            # Restore lists (e.g., the outer list of RowProxy items)
            elif data_type == "list":
                return [
                    self._restore_from_json_compatible(item, f"{path}[{i}]") 
                    for i, item in enumerate(value)
                ]
                
            # Restore dictionaries (for general dict data)
            elif data_type == "dict":
                return {
                    k: self._restore_from_json_compatible(v, f"{path}['{k}']") 
                    for k, v in value.items()
                }
                
            # Restore Decimals
            elif data_type == "Decimal":
                return Decimal(value)
                
            # Restore basic types
            elif data_type == "int":
                return int(value)
            elif data_type == "float":
                return float(value)
            elif data_type == "str":
                return str(value)
            elif data_type == "bool":
                return bool(value)
            elif data_type == "NoneType":
                return None
                
            # Reject unknown type markers
            else:
                raise TypeError(
                    f"Unsupported type marker '{data_type}' at {path}"
                )
                
        except Exception as e:
            raise ValueError(
                f"Restoration failed at {path}: {str(e)}"
            ) from e
    
    def dumps(self, value):
        """Serialize to JSON bytes"""
        try:
            compatible_data = self._convert_to_json_compatible(value)
            return json.dumps(compatible_data).encode()
        except Exception as e:
            raise ValueError(
                f"Serialization failed: {str(e)}\n"
                f"Original value type: {type(value).__name__}"
            ) from e
    
    def loads(self, value):
        """Deserialize from bytes/string (handles both input types)"""
        if value is None:
            return None
            
        try:
            # Handle bytes (decode to string) or direct string input
            json_str = value.decode() if isinstance(value, bytes) else str(value)
            
            if not json_str.strip():
                raise ValueError("Empty input string")
                
            compatible_data = json.loads(json_str)
            return self._restore_from_json_compatible(compatible_data)
            
        except json.JSONDecodeError as e:
            error_context = json_str[max(0, e.pos - 50):e.pos + 50]
            raise ValueError(
                f"Invalid JSON: {str(e)}\n"
                f"Context: ...{error_context}..."
            ) from e
            
        except Exception as e:
            raise ValueError(
                f"Deserialization failed: {str(e)}\n"
                f"Input snippet: {str(value)[:200]}"
            ) from e