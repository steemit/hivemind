import pytest
import json
from decimal import Decimal
from hive.utils.safe_serializer import SafeUniversalSerializer

# Mock RowProxy class for testing (mimicking aiopg's RowProxy)
class MockRowProxy:
    def __init__(self, data):
        self._data = data
    
    def __getitem__(self, key):
        return self._data[key]
    
    def items(self):
        return self._data.items()
    
    def dict(self):
        return self._data.copy()

@pytest.fixture
def serializer():
    """Fixture providing a SafeUniversalSerializer instance"""
    return SafeUniversalSerializer()

def test_basic_types(serializer):
    """Test serialization of basic data types"""
    test_cases = [
        (123, int),
        (3.14159, float),
        ("hello world", str),
        (True, bool),
        (False, bool),
        (None, type(None))
    ]
    
    for value, expected_type in test_cases:
        serialized = serializer.dumps(value)
        assert isinstance(serialized, bytes)
        
        deserialized = serializer.loads(serialized)
        assert deserialized == value
        assert isinstance(deserialized, expected_type)

def test_decimal_type(serializer):
    """Test Decimal type handling"""
    test_values = [
        Decimal("10.5"),
        Decimal("0.00001"),
        Decimal("1000000.123456789")
    ]
    
    for value in test_values:
        serialized = serializer.dumps(value)
        deserialized = serializer.loads(serialized)
        
        assert deserialized == value
        assert isinstance(deserialized, Decimal)

def test_tuples(serializer):
    """Test tuple serialization"""
    test_cases = [
        ((), tuple),
        (("a",), tuple),
        ((1, 2, 3), tuple),
        (("a", 1, True, Decimal("3.14")), tuple)
    ]
    
    for value, expected_type in test_cases:
        serialized = serializer.dumps(value)
        deserialized = serializer.loads(serialized)
        
        assert deserialized == value
        assert isinstance(deserialized, expected_type)

def test_lists(serializer):
    """Test list serialization"""
    test_cases = [
        ([], list),
        ([1, 2, 3], list),
        (["a", "b", "c"], list),
        ([1, "two", True, Decimal("3.14")], list)
    ]
    
    for value, expected_type in test_cases:
        serialized = serializer.dumps(value)
        deserialized = serializer.loads(serialized)
        
        assert deserialized == value
        assert isinstance(deserialized, expected_type)

def test_dictionaries(serializer):
    """Test dictionary serialization"""
    test_cases = [
        ({}, dict),
        ({"name": "test", "value": 42}, dict),
        ({"nested": {"a": 1, "b": 2}}, dict),
        ({"mixed": [1, 2, ("tuple",)], "decimal": Decimal("5.5")}, dict)
    ]
    
    for value, expected_type in test_cases:
        serialized = serializer.dumps(value)
        deserialized = serializer.loads(serialized)
        
        assert deserialized == value
        assert isinstance(deserialized, expected_type)

def test_row_proxy(serializer):
    """Test RowProxy handling"""
    test_data = {
        "id": 1,
        "name": "test",
        "value": Decimal("99.99"),
        "active": True
    }
    row_proxy = MockRowProxy(test_data)
    
    serialized = serializer.dumps(row_proxy)
    deserialized = serializer.loads(serialized)
    
    # Should restore as a dictionary matching original data
    assert deserialized == test_data
    assert isinstance(deserialized, dict)

def test_nested_structures(serializer):
    """Test complex nested structures"""
    test_structures = [
        # List of tuples containing dictionaries
        [
            ("first", {"value": 1, "data": [Decimal("1.1"), Decimal("1.2")]}),
            ("second", {"value": 2, "data": [Decimal("2.1"), Decimal("2.2")]})
        ],
        # Dictionary with nested tuples and lists
        {
            "group": "test",
            "items": (
                ["a", "b", "c"],
                {"x": 10, "y": 20},
                Decimal("30.5")
            )
        }
    ]
    
    for structure in test_structures:
        serialized = serializer.dumps(structure)
        deserialized = serializer.loads(serialized)
        
        assert deserialized == structure
        assert isinstance(deserialized, type(structure))

def test_unsupported_types(serializer):
    """Test handling of unsupported types"""
    class UnsupportedType:
        pass
    
    unsupported_value = UnsupportedType()
    
    with pytest.raises(TypeError) as excinfo:
        serializer.dumps(unsupported_value)
    
    assert "Unsupported type" in str(excinfo.value)

def test_input_handling(serializer):
    """Test handling of different input types to loads()"""
    test_value = {"test": [1, 2, 3], "decimal": Decimal("4.5")}
    
    # Test bytes input
    bytes_data = serializer.dumps(test_value)
    assert isinstance(bytes_data, bytes)
    assert serializer.loads(bytes_data) == test_value
    
    # Test string input
    string_data = bytes_data.decode()
    assert isinstance(string_data, str)
    assert serializer.loads(string_data) == test_value
    
    # Test None input
    assert serializer.loads(None) is None

def test_json_compatibility(serializer):
    """Verify serialized data is valid JSON"""
    test_value = {
        "name": "json test",
        "values": (1, 2, 3),
        "nested": {"decimal": Decimal("100.0001")},
        "active": True
    }
    
    serialized = serializer.dumps(test_value)
    assert isinstance(serialized, bytes)
    
    # Verify JSON validity
    try:
        json.loads(serialized.decode())
    except json.JSONDecodeError:
        pytest.fail("Serialized data is not valid JSON")
