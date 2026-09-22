//! One strict, lossless argument identity for transport and replay evidence.
//! This is not a repair parser. In particular, it never unescapes arbitrary text,
//! overwrites duplicate keys, rounds numbers, or normalizes Unicode characters.

use std::{collections::BTreeMap, fmt};

use serde::{
    Deserialize, Deserializer,
    de::{Error as _, MapAccess, Visitor},
};
use serde_json::{Error, value::RawValue};

struct Object(BTreeMap<String, Box<RawValue>>);

impl<'de> Deserialize<'de> for Object {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct ObjectVisitor;
        impl<'de> Visitor<'de> for ObjectVisitor {
            type Value = Object;
            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("an object with unique decoded keys")
            }
            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Object, A::Error> {
                let mut members = BTreeMap::new();
                while let Some((key, value)) = map.next_entry::<String, Box<RawValue>>()? {
                    if members.insert(key, value).is_some() {
                        return Err(A::Error::custom("duplicate argument key"));
                    }
                }
                Ok(Object(members))
            }
        }
        deserializer.deserialize_map(ObjectVisitor)
    }
}

pub(crate) fn canonical_arguments(raw: &str) -> Result<String, Error> {
    // RawValue validates JSON grammar without converting numbers to f64.
    let value: Box<RawValue> = serde_json::from_str(raw)?;
    if !value.get().starts_with('{') {
        return Err(Error::custom("tool arguments must be a JSON object"));
    }
    canonical_value(&value, 0)
}

fn canonical_value(value: &RawValue, depth: usize) -> Result<String, Error> {
    if depth >= 128 {
        return Err(Error::custom("argument nesting limit exceeded"));
    }
    let raw = value.get();
    match raw.as_bytes()[0] {
        b'{' => {
            let Object(members) = serde_json::from_str(raw)?;
            let mut output = String::from("{");
            for (index, (key, value)) in members.into_iter().enumerate() {
                if index > 0 {
                    output.push(',');
                }
                output.push_str(&serde_json::to_string(&key)?);
                output.push(':');
                output.push_str(&canonical_value(&value, depth + 1)?);
            }
            output.push('}');
            Ok(output)
        }
        b'[' => {
            let values: Vec<Box<RawValue>> = serde_json::from_str(raw)?;
            let mut output = String::from("[");
            for (index, value) in values.into_iter().enumerate() {
                if index > 0 {
                    output.push(',');
                }
                output.push_str(&canonical_value(&value, depth + 1)?);
            }
            output.push(']');
            Ok(output)
        }
        b'"' => {
            // String decoding validates surrogate pairs; literal backslashes
            // remain literal, and canonically different code points stay different.
            serde_json::to_string(&serde_json::from_str::<String>(raw)?)
        }
        b't' | b'f' | b'n' => Ok(raw.to_owned()),
        _ => Ok(canonical_number(raw)),
    }
}

fn canonical_number(raw: &str) -> String {
    let negative = raw.starts_with('-');
    let unsigned = raw.strip_prefix('-').unwrap_or(raw);
    let (mantissa, exponent) = unsigned.split_once(['e', 'E']).unwrap_or((unsigned, "0"));
    // Preserve the int/float distinction observable by the actual Python caller.
    // Integer -0 has no distinct Python value; floating -0.0 does.
    if !unsigned.contains(['.', 'e', 'E']) {
        return if unsigned == "0" {
            "0".to_owned()
        } else {
            raw.to_owned()
        };
    }
    let fractional = mantissa
        .split_once('.')
        .map_or(0, |(_, fraction)| fraction.len());
    let digits = mantissa.replace('.', "");
    let significant = digits.trim_start_matches('0');
    let sign = if negative { "-" } else { "" };
    if significant.is_empty() {
        return format!("{sign}0e0");
    }
    let coefficient = significant.trim_end_matches('0');
    let trailing = significant.len() - coefficient.len();
    let shift = if trailing >= fractional {
        (false, (trailing - fractional).to_string())
    } else {
        (true, (fractional - trailing).to_string())
    };
    let exponent = add_decimal_exponent(exponent, shift.0, &shift.1);
    format!("{sign}{coefficient}e{exponent}")
}

// Exact signed decimal addition. Exponents can exceed every machine integer;
// their spelling is bounded by the request, so no big-number dependency or
// floating conversion is needed. Work and output are linear in input length.
fn add_decimal_exponent(exponent: &str, shift_negative: bool, shift: &str) -> String {
    let negative = exponent.starts_with('-');
    let magnitude = exponent
        .trim_start_matches(['+', '-'])
        .trim_start_matches('0');
    let magnitude = if magnitude.is_empty() { "0" } else { magnitude };
    let mut left = magnitude
        .as_bytes()
        .iter()
        .rev()
        .map(|v| v - b'0')
        .collect::<Vec<_>>();
    let mut right = shift
        .as_bytes()
        .iter()
        .rev()
        .map(|v| v - b'0')
        .collect::<Vec<_>>();
    let same_sign = negative == shift_negative;
    let left_is_larger =
        magnitude.len() > shift.len() || (magnitude.len() == shift.len() && magnitude >= shift);
    let result_negative = if same_sign || left_is_larger {
        negative
    } else {
        shift_negative
    };
    if !same_sign && !left_is_larger {
        std::mem::swap(&mut left, &mut right);
    }
    let mut digits = Vec::new();
    let mut carry: i16 = 0;
    for index in 0..left.len().max(right.len()) {
        let a = i16::from(*left.get(index).unwrap_or(&0));
        let b = i16::from(*right.get(index).unwrap_or(&0));
        let value = if same_sign {
            a + b + carry
        } else {
            a - b + carry
        };
        if same_sign {
            digits.push((value % 10) as u8);
            carry = value / 10;
        } else {
            digits.push(value.rem_euclid(10) as u8);
            carry = if value < 0 { -1 } else { 0 };
        }
    }
    if carry > 0 {
        digits.push(carry as u8);
    }
    while digits.len() > 1 && digits.last() == Some(&0) {
        digits.pop();
    }
    let mut result = String::new();
    if result_negative && digits.iter().any(|digit| *digit != 0) {
        result.push('-');
    }
    result.extend(
        digits
            .into_iter()
            .rev()
            .map(|digit| char::from(b'0' + digit)),
    );
    result
}
