# Dictionaries

Dictionaries have the following schema, in newline-delimited JSON:

```typescript
type Entry = {
    // The word itself.
    //
    // Special characters:
    // - □: This character cannot be represented in Unicode.
    // - ⿰⿱⿲⿳⿴⿵⿶⿷⿸⿹⿺⿻: https://en.wikipedia.org/wiki/Ideographic_Description_Characters_(Unicode_block)
    // - …: This can be substituted for any word, in phrases.
    word: string;

    // All definitions of the word.
    definitions: {
        // All readings for this definition.
        //
        // The placeholder character _ may be present here with the same meaning as in the `word` field.
        readings: string[];

        // All meanings for this definition.
        meanings: string[];
    }[];
};
```
